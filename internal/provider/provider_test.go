package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/go-github/v93/github"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/vmvarela/terraform-provider-github-enterprise/internal/client"
)

var factories = map[string]func() (tfprotov6.ProviderServer, error){"github-enterprise": providerserver.NewProtocol6WithError(New("test")())}

func TestProviderSchema(t *testing.T) {
	s := providerserver.NewProtocol6(New("test")())()
	r, err := s.GetProviderSchema(context.Background(), &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range r.Diagnostics {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			t.Fatalf("%s: %s", d.Summary, d.Detail)
		}
	}
	if len(r.ResourceSchemas) != 5 || len(r.DataSourceSchemas) != 10 {
		t.Fatalf("resources=%d data sources=%d", len(r.ResourceSchemas), len(r.DataSourceSchemas))
	}
}

// This HTTP fixture exercises the provider through the Terraform CLI and plugin
// protocol, not just individual CRUD methods. It intentionally requires no
// enterprise credentials and never calls api.github.com.
type enterpriseAPI struct {
	mu                                      sync.Mutex
	stores                                  map[string]*enterpriseState
	creates, memberWrites, assignmentWrites int
	deny                                    bool
	failRemoval                             bool
	// lastUpdate holds the decoded body of the most recent team write so
	// tests can assert which fields were sent.
	lastUpdate map[string]any
	// removals holds the path of every destructive request, so a deletion
	// sent to the wrong enterprise is visible even when deleting the team
	// clears its relations anyway.
	removals []string
}

// enterpriseState is the remote data of one enterprise, so each slug is
// routed to its own team instead of one shared fixture store.
type enterpriseState struct {
	team    *github.EnterpriseTeam
	members map[string]bool
	orgs    map[string]bool
}

func (a *enterpriseAPI) store(slug string) *enterpriseState {
	st := a.stores[slug]
	if st == nil {
		st = &enterpriseState{members: map[string]bool{}, orgs: map[string]bool{}}
		a.stores[slug] = st
	}
	return st
}

func newAPI(t *testing.T) (*enterpriseAPI, string) {
	t.Helper()
	a := &enterpriseAPI{stores: map[string]*enterpriseState{}}
	s := httptest.NewServer(http.HandlerFunc(a.serve))
	t.Cleanup(s.Close)
	return a, s.URL
}
func (a *enterpriseAPI) serve(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("Authorization") != "Bearer mock-token" {
		http.Error(w, "unauthorized", 401)
		return
	}
	if a.deny {
		http.Error(w, "forbidden", 403)
		return
	}
	segments := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/enterprises/"), "/"), "/")
	// Any enterprise slug is accepted so tests can exercise a provider-level default.
	if len(segments) < 2 || segments[1] != "teams" {
		http.NotFound(w, r)
		return
	}
	st := a.store(segments[0])
	if r.Method == "DELETE" || strings.HasSuffix(r.URL.Path, "/organizations/remove") {
		a.removals = append(a.removals, r.URL.Path)
	}
	parts := segments[2:]
	if len(parts) == 0 {
		if r.Method == "GET" {
			teams := []github.EnterpriseTeam{}
			if st.team != nil {
				teams = append(teams, *st.team)
			}
			_ = json.NewEncoder(w).Encode(teams)
			return
		}
		if r.Method == "POST" {
			a.creates++
			st.team = &github.EnterpriseTeam{ID: 42}
			a.updateTeam(st, r)
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(st.team)
			return
		}
	}
	if st.team == nil {
		http.NotFound(w, r)
		return
	}
	if parts[0] != "42" && parts[0] != st.team.Slug {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 1 {
		switch r.Method {
		case "GET":
			_ = json.NewEncoder(w).Encode(st.team)
		case "PATCH":
			a.updateTeam(st, r)
			_ = json.NewEncoder(w).Encode(st.team)
		case "DELETE":
			st.team = nil
			st.members = map[string]bool{}
			st.orgs = map[string]bool{}
			w.WriteHeader(204)
		default:
			http.Error(w, "method", 405)
		}
		return
	}
	if parts[1] == "memberships" && len(parts) == 3 {
		user := parts[2]
		switch r.Method {
		case "PUT":
			if st.team.GroupID != "" {
				http.Error(w, "IdP membership", 422)
				return
			}
			st.members[user] = true
			a.memberWrites++
			w.WriteHeader(201)
		case "GET":
			if !st.members[user] {
				http.NotFound(w, r)
				return
			}
		case "DELETE":
			delete(st.members, user)
			a.memberWrites++
			w.WriteHeader(204)
			return
		default:
			http.Error(w, "method", 405)
			return
		}
		_ = json.NewEncoder(w).Encode(github.User{Login: github.String(user), ID: github.Int64(7)})
		return
	}
	if parts[1] == "organizations" {
		if len(parts) == 3 && r.Method == "POST" {
			if st.team.OrganizationSelectionType == nil || *st.team.OrganizationSelectionType != "selected" {
				http.Error(w, "mode", 422)
				return
			}
			if parts[2] == "remove" && a.failRemoval {
				a.failRemoval = false
				http.Error(w, "temporary error", 503)
				return
			}
			var body struct {
				Slugs []string `json:"organization_slugs"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			for _, s := range body.Slugs {
				if parts[2] == "add" {
					st.orgs[s] = true
				} else {
					delete(st.orgs, s)
				}
			}
			a.assignmentWrites++
			if parts[2] == "remove" {
				w.WriteHeader(204)
				return
			}
		}
		orgs := []github.Organization{}
		names := []string{}
		for s := range st.orgs {
			names = append(names, s)
		}
		sort.Strings(names)
		for _, s := range names {
			orgs = append(orgs, github.Organization{Login: github.String(s)})
		}
		_ = json.NewEncoder(w).Encode(orgs)
		return
	}
	http.NotFound(w, r)
}
func (a *enterpriseAPI) updateTeam(st *enterpriseState, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	a.lastUpdate = body
	if v, ok := body["name"].(string); ok {
		st.team.Name = v
		st.team.Slug = "ent:" + strings.ReplaceAll(strings.ToLower(v), " ", "-")
	}
	if v, ok := body["description"].(string); ok {
		st.team.Description = github.String(v)
	}
	if v, ok := body["organization_selection_type"].(string); ok {
		st.team.OrganizationSelectionType = new(v)
	}
	if _, ok := body["group_id"]; ok {
		// Simulates GitHub: sending group_id explicitly, including a null
		// unlink, replaces direct team membership.
		st.members = map[string]bool{}
		st.team.GroupID = ""
		if s, ok := body["group_id"].(string); ok {
			st.team.GroupID = s
		}
	}
}
func requireTerraform(t *testing.T) {
	t.Helper()
	executable := os.Getenv("TF_ACC_TERRAFORM_PATH")
	if executable == "" {
		executable = "terraform"
	}
	if _, err := exec.LookPath(executable); err != nil {
		if os.Getenv("TF_REQUIRE_CLI") == "1" {
			t.Fatalf("CLI integration is required but %q is unavailable: %v", executable, err)
		}
		t.Skip("Install Terraform or set TF_ACC_TERRAFORM_PATH to run CLI integration tests")
	}
}

func config(base, name, description, group, orgs string) string {
	return fmt.Sprintf(`
provider "github-enterprise" {
 token = "mock-token"
 base_url = %q
}
resource "github_enterprise_team" "test" {
  provider = github-enterprise

 enterprise_slug = "acme"
 name = %q
 description = %q
 group_id = %q
 organization_selection_type = "selected"
}
resource "github_enterprise_team_membership" "test" {
  provider = github-enterprise

 enterprise_slug = "acme"
 team_id = github_enterprise_team.test.team_id
 username = "octocat"
}
resource "github_enterprise_team_organizations" "test" {
  provider = github-enterprise

 enterprise_slug = "acme"
 team_id = github_enterprise_team.test.team_id
 organization_slugs = %s
}
data "github_enterprise_team" "test" {
  provider = github-enterprise

 enterprise_slug = "acme"
 slug = github_enterprise_team.test.slug
}
data "github_enterprise_teams" "test" {
  provider = github-enterprise

 enterprise_slug = "acme"
 depends_on = [github_enterprise_team.test]
}
data "github_enterprise_team_membership" "test" {
  provider = github-enterprise

 enterprise_slug = "acme"
 team_id = github_enterprise_team.test.team_id
 username = github_enterprise_team_membership.test.username
}
data "github_enterprise_team_organizations" "test" {
  provider = github-enterprise

 enterprise_slug = "acme"
 team_id = github_enterprise_team.test.team_id
 depends_on = [github_enterprise_team_organizations.test]
}
`, base, name, description, group, orgs)
}

func TestTerraformLifecycle(t *testing.T) {
	requireTerraform(t)
	api, base := newAPI(t)
	checks := resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("github_enterprise_team.test", "team_id", "42"), resource.TestCheckResourceAttr("github_enterprise_team_membership.test", "user_id", "7"), resource.TestCheckResourceAttr("data.github_enterprise_team_membership.test", "is_member", "true"), resource.TestCheckResourceAttr("data.github_enterprise_teams.test", "teams.#", "1"))
	initial := config(base, "Platform", "Original description", "", `["org-one"]`)
	renamed := config(base, "New Platform", "", "", `["org-two"]`)
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: factories, CheckDestroy: func(_ *terraform.State) error {
		api.mu.Lock()
		defer api.mu.Unlock()
		if api.store("acme").team != nil || len(api.store("acme").members) != 0 || len(api.store("acme").orgs) != 0 {
			return fmt.Errorf("remote resources remain")
		}
		return nil
	}, Steps: []resource.TestStep{
		{Config: initial, Check: checks},
		{ResourceName: "github_enterprise_team.test", ImportState: true, ImportStateVerify: true},
		{ResourceName: "github_enterprise_team_membership.test", ImportState: true, ImportStateVerify: true},
		{ResourceName: "github_enterprise_team_organizations.test", ImportState: true, ImportStateVerify: true},
		{Config: renamed, Check: resource.ComposeTestCheckFunc(checks, resource.TestCheckResourceAttr("github_enterprise_team.test", "slug", "ent:new-platform"), resource.TestCheckResourceAttr("github_enterprise_team.test", "description", ""))},
		{Config: renamed, PreConfig: func() {
			api.mu.Lock()
			defer api.mu.Unlock()
			if api.creates != 1 || api.memberWrites != 1 {
				t.Errorf("rename recreated team or membership: creates=%d writes=%d", api.creates, api.memberWrites)
			}
			api.store("acme").orgs["external-org"] = true
			delete(api.store("acme").members, "octocat")
		}, Check: resource.TestCheckResourceAttr("github_enterprise_team_organizations.test", "organization_slugs.#", "1")},
		{Config: config(base, "New Platform", "", "", `[]`), Check: resource.TestCheckResourceAttr("github_enterprise_team_organizations.test", "organization_slugs.#", "0")},
	}})
}

// A single provider-level slug must be enough for every block, and changing it
// must replace the resources instead of updating them against the old enterprise.
func TestTerraformInheritsProviderEnterpriseSlug(t *testing.T) {
	requireTerraform(t)
	api, base := newAPI(t)
	cfg := func(slug string) string {
		return fmt.Sprintf(`
provider "github-enterprise" {
  token           = "mock-token"
  base_url        = %q
  enterprise_slug = %q
}
resource "github_enterprise_team" "test" {
  provider = github-enterprise

  name                        = "Platform"
  organization_selection_type = "selected"
}
resource "github_enterprise_team_membership" "test" {
  provider = github-enterprise

  team_id  = github_enterprise_team.test.team_id
  username = "octocat"
}
resource "github_enterprise_team_organizations" "test" {
  provider = github-enterprise

  team_id            = github_enterprise_team.test.team_id
  organization_slugs = ["org-one"]
}
data "github_enterprise_teams" "test" {
  provider = github-enterprise

  depends_on = [github_enterprise_team.test]
}
`, base, slug)
	}
	inherited := func(slug string) resource.TestCheckFunc {
		return resource.ComposeTestCheckFunc(
			resource.TestCheckResourceAttr("github_enterprise_team.test", "enterprise_slug", slug),
			resource.TestCheckResourceAttr("github_enterprise_team_membership.test", "enterprise_slug", slug),
			resource.TestCheckResourceAttr("github_enterprise_team_organizations.test", "enterprise_slug", slug),
			resource.TestCheckResourceAttr("data.github_enterprise_teams.test", "enterprise_slug", slug),
			// A data source that reported the new slug but queried the old
			// enterprise would return no teams after the migration.
			resource.TestCheckResourceAttr("data.github_enterprise_teams.test", "teams.#", "1"),
		)
	}
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: factories, Steps: []resource.TestStep{
		{Config: cfg("acme"), Check: inherited("acme")},
		{Config: cfg("acme-beta"), Check: resource.ComposeTestCheckFunc(inherited("acme-beta"), func(_ *terraform.State) error {
			api.mu.Lock()
			defer api.mu.Unlock()
			if api.creates != 2 {
				return fmt.Errorf("changing the provider slug did not replace the team: creates=%d", api.creates)
			}
			old, next := api.store("acme"), api.store("acme-beta")
			if old.team != nil || len(old.members) != 0 || len(old.orgs) != 0 {
				return fmt.Errorf("old enterprise acme still holds resources: team=%v members=%v orgs=%v", old.team, old.members, old.orgs)
			}
			if next.team == nil {
				return fmt.Errorf("new enterprise acme-beta has no team")
			}
			if next.team.ID != 42 {
				return fmt.Errorf("new enterprise acme-beta team ID = %d, want 42", next.team.ID)
			}
			if !next.members["octocat"] {
				return fmt.Errorf("new enterprise acme-beta is missing the octocat membership: %v", next.members)
			}
			if len(next.orgs) != 1 || !next.orgs["org-one"] {
				return fmt.Errorf("new enterprise acme-beta is missing organization org-one: %v", next.orgs)
			}
			if len(api.removals) == 0 {
				return fmt.Errorf("no removal was recorded, so the old enterprise was never addressed")
			}
			var memberRemoval, orgRemoval bool
			for _, path := range api.removals {
				if !strings.HasPrefix(path, "/enterprises/acme/") {
					return fmt.Errorf("removal hit the wrong enterprise: %s", path)
				}
				memberRemoval = memberRemoval || strings.Contains(path, "/memberships/")
				orgRemoval = orgRemoval || strings.HasSuffix(path, "/organizations/remove")
			}
			// Deleting the team also clears its relations, so a membership or
			// organization delete aimed at the new enterprise would otherwise
			// hide behind that cleanup.
			if !memberRemoval || !orgRemoval {
				return fmt.Errorf("membership or organization removal missed the old enterprise: %v", api.removals)
			}
			return nil
		})},
	}})
}

func TestTerraformOutOfBandRename(t *testing.T) {
	requireTerraform(t)
	api, base := newAPI(t)
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: factories, Steps: []resource.TestStep{
		{Config: config(base, "Platform", "", "", `[]`)},
		{Config: config(base, "Platform", "", "", `[]`), PreConfig: func() {
			api.mu.Lock()
			defer api.mu.Unlock()
			api.store("acme").team.Name = "Renamed elsewhere"
			api.store("acme").team.Slug = "ent:renamed-elsewhere"
		}, Check: resource.TestCheckResourceAttr("github_enterprise_team.test", "slug", "ent:platform")},
	}})
}

func TestTerraformGroupUnlink(t *testing.T) {
	requireTerraform(t)
	api, base := newAPI(t)
	teamConfig := func(group string) string {
		return fmt.Sprintf(`provider "github-enterprise" {
 token="mock-token"
base_url=%q
}
resource "github_enterprise_team" "test" {
  provider = github-enterprise

 enterprise_slug="acme"
name="Platform"
%s
}`, base, group)
	}
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: factories, Steps: []resource.TestStep{
		{Config: teamConfig(`group_id="group-123"`)},
		{Config: teamConfig(""), Check: func(_ *terraform.State) error {
			api.mu.Lock()
			defer api.mu.Unlock()
			if api.creates != 1 || api.store("acme").team.GroupID != "" {
				return fmt.Errorf("group was not unlinked in place")
			}
			return nil
		}},
	}})
}

// Simulates GitHub removing direct members when group_id is sent explicitly,
// and proves an unrelated update omits group_id so a test-created direct
// member is preserved. The fixture never deletes data outside its in-memory
// instance.
func TestTerraformGroupUpdateMemberSafety(t *testing.T) {
	requireTerraform(t)
	api, base := newAPI(t)
	cfg := func(name, group, member string) string {
		membership := ""
		if member != "" {
			membership = fmt.Sprintf(`
resource "github_enterprise_team_membership" "test" {
  provider = github-enterprise

 enterprise_slug="acme"
team_id=github_enterprise_team.test.team_id
username=%q
}`, member)
		}
		return fmt.Sprintf(`provider "github-enterprise" {
 token="mock-token"
base_url=%q
}
resource "github_enterprise_team" "test" {
  provider = github-enterprise

 enterprise_slug="acme"
name=%q
group_id=%q
}%s`, base, name, group, membership)
	}
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: factories, Steps: []resource.TestStep{
		{Config: cfg("Platform", "", "octocat")},
		{Config: cfg("Renamed Platform", "", "octocat"), Check: func(_ *terraform.State) error {
			api.mu.Lock()
			defer api.mu.Unlock()
			if _, sent := api.lastUpdate["group_id"]; sent {
				return fmt.Errorf("unrelated update sent group_id: %v", api.lastUpdate)
			}
			if !api.store("acme").members["octocat"] {
				return fmt.Errorf("direct member lost on unrelated update")
			}
			return nil
		}},
		// An explicit group change may replace direct members server-side.
		// The membership resource is dropped from configuration first so no
		// reconcile obscures the observation, then PreConfig seeds a
		// test-only direct member so the update provably removes a member
		// that is present when the group change is sent.
		{Config: cfg("Renamed Platform", "group-123", ""), PreConfig: func() {
			api.mu.Lock()
			defer api.mu.Unlock()
			api.store("acme").members["out-of-band-member"] = true
		}, Check: func(_ *terraform.State) error {
			api.mu.Lock()
			defer api.mu.Unlock()
			if v, sent := api.lastUpdate["group_id"]; !sent || v != "group-123" {
				return fmt.Errorf("expected an explicit group_id value, got %v", api.lastUpdate)
			}
			if len(api.store("acme").members) != 0 {
				return fmt.Errorf("group change did not clear the present direct member: %v", api.store("acme").members)
			}
			return nil
		}},
		{Config: cfg("Renamed Platform", "", ""), Check: func(_ *terraform.State) error {
			api.mu.Lock()
			defer api.mu.Unlock()
			if v, sent := api.lastUpdate["group_id"]; !sent || v != nil {
				return fmt.Errorf("expected an explicit group_id null on unlink, got %v", api.lastUpdate)
			}
			return nil
		}},
	}})
}

func TestTerraformRefusesToOverwriteOrganizations(t *testing.T) {
	requireTerraform(t)
	api, base := newAPI(t)
	api.store("acme").team = &github.EnterpriseTeam{ID: 42, Name: "Platform", Slug: "ent:platform", OrganizationSelectionType: new("selected")}
	api.store("acme").orgs["existing-org"] = true
	cfg := fmt.Sprintf(`provider "github-enterprise" {
 token="mock-token"
base_url=%q
}
resource "github_enterprise_team_organizations" "test" {
  provider = github-enterprise

 enterprise_slug="acme"
team_id=42
organization_slugs=["org-one"]
}`, base)
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: factories, Steps: []resource.TestStep{{Config: cfg, ExpectError: regexp.MustCompile("Existing organization assignments")}}})
	api.mu.Lock()
	defer api.mu.Unlock()
	if api.assignmentWrites != 0 || !api.store("acme").orgs["existing-org"] {
		t.Fatal("existing assignments changed")
	}
}

func TestImportValidation(t *testing.T) {
	for _, id := range []string{":42", "acme:0", "acme:-1", "acme:01", "acme:9223372036854775808", "acme:ent:platform", "acme:42:extra", "acme/42"} {
		if _, err := parseImportID(id, 2); err == nil {
			t.Errorf("accepted %q", id)
		}
	}
	for _, id := range []string{"acme:42:", "acme:42:user:path", "acme:42:..", "acme/42/octocat"} {
		if _, err := parseImportID(id, 3); err == nil {
			t.Errorf("accepted %q", id)
		}
	}
}

// Live acceptance tests are opt-in and fail closed. Their safety model:
//
//   - TF_ACC must be exactly "1" and GITHUB_ENTERPRISE_TEST_CONFIRM must
//     exactly match GITHUB_ENTERPRISE_SLUG in the same command invocation.
//     The confirmation belongs on the command line, never in .envrc or shell
//     profiles.
//   - The configured API host must be GitHub Enterprise Cloud (api.github.com
//     or *.ghe.com); loopback and other hosts are refused before any write.
//   - A read-only listing of existing team IDs is captured before creation.
//     Only a team whose numeric ID is absent from that baseline is accepted,
//     and destroy verification checks that exact ID is gone. Teams are never
//     adopted, discovered or deleted by name; import steps use only IDs from
//     the same test's state.
//   - There is no sweeper. If the process is killed during a
//     network-ambiguous step, a test team can remain; inspect the enterprise
//     and report or delete it manually rather than guessing.
func liveGate(t *testing.T) string {
	t.Helper()
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("Set TF_ACC=1 (exactly; any other value skips) to run live acceptance tests")
	}
	enterprise := os.Getenv("GITHUB_ENTERPRISE_SLUG")
	if enterprise == "" {
		t.Fatal("GITHUB_ENTERPRISE_SLUG is required")
	}
	confirm := os.Getenv("GITHUB_ENTERPRISE_TEST_CONFIRM")
	if confirm != enterprise {
		t.Fatalf("GITHUB_ENTERPRISE_TEST_CONFIRM must be set to exactly %q in the same command to confirm the intended enterprise; got %q", enterprise, confirm)
	}
	return enterprise
}

// validateLiveBaseURL accepts only an HTTPS GitHub Enterprise Cloud URL:
// api.github.com or *.ghe.com, default port, no userinfo, path, query or
// fragment. Error text never echoes credentials.
func validateLiveBaseURL(base string) error {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		u.Hostname() == "" || (u.Port() != "" && u.Port() != "443") ||
		(u.EscapedPath() != "" && u.EscapedPath() != "/") {
		return fmt.Errorf("must be an HTTPS GitHub Enterprise Cloud URL without userinfo, non-default port, path, query or fragment")
	}
	host := strings.ToLower(u.Hostname())
	if host != "api.github.com" && (!strings.HasSuffix(host, ".ghe.com") || strings.HasPrefix(host, ".")) {
		return fmt.Errorf("%q is not GitHub Enterprise Cloud (api.github.com or *.ghe.com)", host)
	}
	return nil
}

// liveBaseURL resolves the API host from the environment and refuses
// anything that is not GitHub Enterprise Cloud before any request is made.
// It never prints credentials and never sets or overrides authentication
// variables.
func liveBaseURL(t *testing.T) string {
	t.Helper()
	base := os.Getenv("GITHUB_ENTERPRISE_BASE_URL")
	if base == "" {
		base = "https://api.github.com/"
	}
	if err := validateLiveBaseURL(base); err != nil {
		t.Fatalf("live acceptance refused GITHUB_ENTERPRISE_BASE_URL: %v", err)
	}
	return base
}

// Offline check of the live base-URL gate; it never touches the network.
func TestLiveBaseURLValidation(t *testing.T) {
	allowed := []string{"https://api.github.com/", "https://api.github.com", "https://api.ghe.com:443/", "https://api.acme.ghe.com/"}
	rejected := []string{
		"", "not a url", "ftp://api.github.com/", "http://api.github.com/",
		"https://user:pass@api.github.com/", "https://api.github.com:8443/",
		"https://api.github.com/x/", "https://api.github.com/?x=1", "https://api.github.com/#f",
		"https://ghes.example.com/", "https://api.github.com./", "https://.ghe.com/",
		"http://localhost:1234/",
	}
	for _, base := range allowed {
		if err := validateLiveBaseURL(base); err != nil {
			t.Errorf("expected %q to be allowed: %v", base, err)
		}
	}
	for _, base := range rejected {
		if err := validateLiveBaseURL(base); err == nil {
			t.Errorf("expected %q to be rejected", base)
		}
	}
}

func liveClient(t *testing.T) *client.Client {
	t.Helper()
	token := os.Getenv("GITHUB_ENTERPRISE_TOKEN")
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	if token == "" {
		t.Fatal("GITHUB_ENTERPRISE_TOKEN is required")
	}
	c, err := client.New(liveBaseURL(t), token, client.DefaultAPIVersion)
	if err != nil {
		t.Fatalf("live acceptance client: %v", err)
	}
	return c
}

// teamBaseline records the numeric IDs that already exist so the test only
// ever accepts a team it created itself.
func teamBaseline(t *testing.T, c *client.Client, enterprise string) map[int64]bool {
	t.Helper()
	teams, err := c.Teams(context.Background(), enterprise)
	if err != nil {
		t.Fatalf("list existing enterprise teams: %v", err)
	}
	ids := make(map[int64]bool, len(teams))
	for _, team := range teams {
		ids[team.ID] = true
	}
	return ids
}

type liveTeam struct {
	id   int64
	slug string
}

func primaryState(state *terraform.State, name string) (*terraform.InstanceState, error) {
	rs, ok := state.RootModule().Resources[name]
	if !ok {
		return nil, fmt.Errorf("%s not found in state", name)
	}
	return rs.Primary, nil
}

// captureTeam reads the exact identity of the test-created team from state.
func captureTeam(created *liveTeam) resource.TestCheckFunc {
	return func(state *terraform.State) error {
		p, err := primaryState(state, "github_enterprise_team.test")
		if err != nil {
			return err
		}
		id, err := strconv.ParseInt(p.Attributes["team_id"], 10, 64)
		if err != nil || id <= 0 {
			return fmt.Errorf("invalid team_id %q in state", p.Attributes["team_id"])
		}
		created.id = id
		created.slug = p.Attributes["slug"]
		return nil
	}
}

func checkNewTeamID(created *liveTeam, baseline map[int64]bool) resource.TestCheckFunc {
	return func(_ *terraform.State) error {
		if created.id <= 0 || created.slug == "" {
			return fmt.Errorf("created team identity was not recorded")
		}
		if baseline[created.id] {
			return fmt.Errorf("team %d already existed before the test; refusing to manage it", created.id)
		}
		return nil
	}
}

func checkTeamIDStable(created *liveTeam) resource.TestCheckFunc {
	return func(state *terraform.State) error {
		p, err := primaryState(state, "github_enterprise_team.test")
		if err != nil {
			return err
		}
		if id, _ := strconv.ParseInt(p.Attributes["team_id"], 10, 64); id != created.id {
			return fmt.Errorf("numeric team ID changed from %d to %s across an update", created.id, p.Attributes["team_id"])
		}
		return nil
	}
}

func checkTeamSlugStable(created *liveTeam) resource.TestCheckFunc {
	return func(state *terraform.State) error {
		p, err := primaryState(state, "github_enterprise_team.test")
		if err != nil {
			return err
		}
		if got := p.Attributes["slug"]; got != created.slug {
			return fmt.Errorf("team slug changed from %q to %q", created.slug, got)
		}
		return nil
	}
}

// checkTeamGone verifies the exact pre-destroy numeric ID is absent. It never
// deletes anything; an orphan must be inspected and resolved manually.
func checkTeamGone(c *client.Client, enterprise string, created *liveTeam) func(*terraform.State) error {
	return func(_ *terraform.State) error {
		if created.id <= 0 {
			return fmt.Errorf("destroy verification has no test-created team ID")
		}
		for attempt := 0; attempt < 15; attempt++ {
			teams, err := c.Teams(context.Background(), enterprise)
			if err != nil {
				return fmt.Errorf("verify removal of team %d: %w", created.id, err)
			}
			found := false
			for _, team := range teams {
				if team.ID == created.id {
					found = true
					break
				}
			}
			if !found {
				return nil
			}
			if attempt < 14 {
				time.Sleep(2 * time.Second)
			}
		}
		return fmt.Errorf("test team %d (slug %q at deletion) still exists after 30 seconds; inspect the enterprise and resolve it manually", created.id, created.slug)
	}
}

func checkTeamExists(c *client.Client, enterprise string, created *liveTeam) resource.TestCheckFunc {
	return func(_ *terraform.State) error {
		if _, err := c.FindTeam(context.Background(), enterprise, created.id, created.slug); err != nil {
			return fmt.Errorf("test team %d must still exist: %w", created.id, err)
		}
		return nil
	}
}

// checkTeamsContainsID asserts the new numeric team ID is present in the list
// without asserting the enterprise's total team count.
func checkTeamsContainsID(dataSource string, created *liveTeam) resource.TestCheckFunc {
	return func(state *terraform.State) error {
		if created.id <= 0 {
			return fmt.Errorf("no test-created team ID to look for")
		}
		p, err := primaryState(state, dataSource)
		if err != nil {
			return err
		}
		for key, value := range p.Attributes {
			if strings.HasPrefix(key, "teams.") && strings.HasSuffix(key, ".team_id") && value == strconv.FormatInt(created.id, 10) {
				return nil
			}
		}
		return fmt.Errorf("%s: test team %d not present", dataSource, created.id)
	}
}

func checkOrgSlugs(dataSource string, want ...string) resource.TestCheckFunc {
	return func(state *terraform.State) error {
		p, err := primaryState(state, dataSource)
		if err != nil {
			return err
		}
		got := map[string]bool{}
		for key, value := range p.Attributes {
			if strings.HasPrefix(key, "organization_slugs.") && !strings.HasSuffix(key, "#") {
				got[value] = true
			}
		}
		if len(got) != len(want) {
			return fmt.Errorf("%s: expected organizations %v, got %v", dataSource, want, got)
		}
		for _, w := range want {
			if !got[w] {
				return fmt.Errorf("%s: expected organizations %v, got %v", dataSource, want, got)
			}
		}
		return nil
	}
}

func checkOrgSlugsRemote(c *client.Client, enterprise string, created *liveTeam, want ...string) resource.TestCheckFunc {
	return func(_ *terraform.State) error {
		if created.id <= 0 {
			return fmt.Errorf("no test-created team ID")
		}
		orgs, err := c.Organizations(context.Background(), enterprise, created.id)
		if err != nil {
			return err
		}
		got := map[string]bool{}
		for _, o := range orgs {
			got[strings.ToLower(o.GetLogin())] = true
		}
		if len(got) != len(want) {
			return fmt.Errorf("remote assignments for team %d: expected %v, got %v", created.id, want, got)
		}
		for _, w := range want {
			if !got[w] {
				return fmt.Errorf("remote assignments for team %d: expected %v, got %v", created.id, want, got)
			}
		}
		return nil
	}
}

func checkMembershipAbsent(c *client.Client, enterprise string, created *liveTeam, username string) resource.TestCheckFunc {
	return func(_ *terraform.State) error {
		if _, err := c.Membership(context.Background(), enterprise, created.id, username); !client.IsNotFound(err) {
			return fmt.Errorf("membership of team %d for the fixture user should be absent (err: %v)", created.id, err)
		}
		return nil
	}
}

// Core team suite: create, read, out-of-band rename reconciliation, rename,
// description clear, import and a stable numeric ID. No group_id is involved.
// The team and both data sources are exercised without asserting
// enterprise-wide totals.
func TestAccTeam(t *testing.T) {
	enterprise := liveGate(t)
	c := liveClient(t)
	baseline := teamBaseline(t, c, enterprise)
	created := &liveTeam{}
	name := "tf-acc-" + acctest.RandStringFromCharSet(10, acctest.CharSetAlphaNum)
	cfg := func(n, desc string) string {
		return fmt.Sprintf(`provider "github-enterprise" {}
resource "github_enterprise_team" "test" {
  provider = github-enterprise

  enterprise_slug = %q
  name            = %q
  description     = %q
}
data "github_enterprise_team" "test" {
  provider = github-enterprise

  enterprise_slug = %q
  slug            = github_enterprise_team.test.slug
  depends_on      = [github_enterprise_team.test]
}
data "github_enterprise_teams" "test" {
  provider = github-enterprise

  enterprise_slug = %q
  depends_on      = [github_enterprise_team.test]
}`, enterprise, n, desc, enterprise, enterprise)
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories, CheckDestroy: checkTeamGone(c, enterprise, created), Steps: []resource.TestStep{
		{Config: cfg(name, "Acceptance test"), Check: resource.ComposeTestCheckFunc(
			captureTeam(created),
			checkNewTeamID(created, baseline),
			resource.TestCheckResourceAttr("data.github_enterprise_team.test", "name", name),
			checkTeamsContainsID("data.github_enterprise_teams.test", created),
		)},
		// Drift: an out-of-band rename of the exact test-created team is
		// reconciled back to the configured name, with the numeric ID stable.
		{Config: cfg(name, "Acceptance test"), PreConfig: func() {
			if _, err := c.UpdateTeam(context.Background(), enterprise, created.slug, map[string]any{"name": name + "-drift"}); err != nil {
				t.Errorf("out-of-band team rename failed: %v", err)
			}
		}, Check: resource.ComposeTestCheckFunc(
			checkTeamIDStable(created),
			resource.TestCheckResourceAttr("github_enterprise_team.test", "name", name),
			checkTeamSlugStable(created),
		)},
		{ResourceName: "github_enterprise_team.test", ImportState: true, ImportStateVerify: true},
		{Config: cfg(name+"-renamed", ""), Check: resource.ComposeTestCheckFunc(
			checkTeamIDStable(created),
			resource.TestCheckResourceAttr("github_enterprise_team.test", "description", ""),
		)},
	}})
}

// Membership suite: runs only with an explicit disposable, approved test
// user, confirmed by GITHUB_ENTERPRISE_TEST_USER_CONFIRM exactly matching the
// username. It manages the membership of the team this test created, never an
// existing one. The team is created and its identity verified against the
// baseline before the membership is written; an out-of-band removal is
// reconciled by Terraform. Destroy order is membership first, then team,
// because the membership resource references the team's generated team_id.
func TestAccTeamMembership(t *testing.T) {
	enterprise := liveGate(t)
	username := os.Getenv("GITHUB_ENTERPRISE_TEST_USERNAME")
	if username == "" {
		t.Fatal("GITHUB_ENTERPRISE_TEST_USERNAME is required for the membership suite")
	}
	if confirm := os.Getenv("GITHUB_ENTERPRISE_TEST_USER_CONFIRM"); confirm != username {
		t.Fatalf("GITHUB_ENTERPRISE_TEST_USER_CONFIRM must be set to exactly %q in the same command to confirm the test user is a disposable, approved fixture account; got %q", username, confirm)
	}
	c := liveClient(t)
	baseline := teamBaseline(t, c, enterprise)
	created := &liveTeam{}
	name := "tf-acc-" + acctest.RandStringFromCharSet(10, acctest.CharSetAlphaNum)
	cfg := func(withMember bool) string {
		member := ""
		if withMember {
			member = fmt.Sprintf(`
resource "github_enterprise_team_membership" "test" {
  provider = github-enterprise

  enterprise_slug = %q
  team_id         = github_enterprise_team.test.team_id
  username        = %q
}
data "github_enterprise_team_membership" "test" {
  provider = github-enterprise

  enterprise_slug = %q
  team_id         = github_enterprise_team.test.team_id
  username        = github_enterprise_team_membership.test.username
  depends_on      = [github_enterprise_team_membership.test]
}`, enterprise, username, enterprise)
		}
		return fmt.Sprintf(`provider "github-enterprise" {}
resource "github_enterprise_team" "test" {
  provider = github-enterprise

  enterprise_slug = %q
  name            = %q
}%s`, enterprise, name, member)
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories, CheckDestroy: checkTeamGone(c, enterprise, created), Steps: []resource.TestStep{
		// Create the team alone and verify its identity against the baseline
		// before any relationship is written.
		{Config: cfg(false), Check: resource.ComposeTestCheckFunc(
			captureTeam(created),
			checkNewTeamID(created, baseline),
		)},
		{Config: cfg(true), Check: resource.ComposeTestCheckFunc(
			checkTeamIDStable(created),
			resource.TestCheckResourceAttrSet("github_enterprise_team_membership.test", "user_id"),
			resource.TestCheckResourceAttr("data.github_enterprise_team_membership.test", "is_member", "true"),
		)},
		// Drift: the membership of the confirmed disposable user is removed
		// out-of-band from this new team only, and Terraform recreates it.
		{Config: cfg(true), PreConfig: func() {
			if err := c.RemoveMember(context.Background(), enterprise, created.id, username); err != nil {
				t.Errorf("out-of-band membership removal failed: %v", err)
			}
		}, Check: resource.ComposeTestCheckFunc(
			checkTeamIDStable(created),
			resource.TestCheckResourceAttrSet("github_enterprise_team_membership.test", "user_id"),
			resource.TestCheckResourceAttr("data.github_enterprise_team_membership.test", "is_member", "true"),
		)},
		{ResourceName: "github_enterprise_team.test", ImportState: true, ImportStateVerify: true},
		{ResourceName: "github_enterprise_team_membership.test", ImportState: true, ImportStateVerify: true,
			ImportStateIdFunc: func(_ *terraform.State) (string, error) {
				return fmt.Sprintf("%s/%d/%s", enterprise, created.id, username), nil
			}},
		// The membership is removed while the team remains; the team is only
		// destroyed afterwards by TestCase teardown.
		{Config: cfg(false), Check: resource.ComposeTestCheckFunc(
			checkMembershipAbsent(c, enterprise, created, username),
			checkTeamExists(c, enterprise, created),
		)},
	}})
}

// Organization assignment suite: runs only with two explicitly configured
// dedicated test organizations plus a fixture confirmation. It creates a new,
// empty team in "selected" mode and never touches other teams or enterprise
// totals. Create-time refusal of pre-existing assignments is covered by
// fixtures, not verified live: a live rehearsal would require an existing
// nonempty team. IdP groups are out of scope for this suite.
func TestAccTeamOrganizations(t *testing.T) {
	enterprise := liveGate(t)
	orgA := os.Getenv("GITHUB_ENTERPRISE_TEST_ORG_A")
	orgB := os.Getenv("GITHUB_ENTERPRISE_TEST_ORG_B")
	if orgA == "" || orgB == "" || orgA == orgB {
		t.Fatal("Set GITHUB_ENTERPRISE_TEST_ORG_A and GITHUB_ENTERPRISE_TEST_ORG_B to two distinct, dedicated test organizations")
	}
	// The confirmation is bound to the exact fixture identity: it must
	// reproduce the ordered, distinct pair being configured.
	if confirm := os.Getenv("GITHUB_ENTERPRISE_TEST_ORGS_CONFIRM"); confirm != orgA+","+orgB {
		t.Fatalf("GITHUB_ENTERPRISE_TEST_ORGS_CONFIRM must be set to exactly %q in the same command to confirm both organizations are dedicated test fixtures; got %q", orgA+","+orgB, confirm)
	}
	orgA = strings.ToLower(orgA)
	orgB = strings.ToLower(orgB)
	if orgA == orgB {
		t.Fatal("the two test organization slugs must remain distinct after lowercasing")
	}
	c := liveClient(t)
	baseline := teamBaseline(t, c, enterprise)
	created := &liveTeam{}
	name := "tf-acc-" + acctest.RandStringFromCharSet(10, acctest.CharSetAlphaNum)
	teamCfg := fmt.Sprintf(`provider "github-enterprise" {}
resource "github_enterprise_team" "test" {
  provider = github-enterprise

  enterprise_slug = %q
  name            = %q

  organization_selection_type = "selected"
}`, enterprise, name)
	cfg := func(orgs string) string {
		return fmt.Sprintf(`%s
resource "github_enterprise_team_organizations" "test" {
  provider = github-enterprise

  enterprise_slug = %q
  team_id         = github_enterprise_team.test.team_id

  organization_slugs = %s
}
data "github_enterprise_team_organizations" "test" {
  provider = github-enterprise

  enterprise_slug = %q
  team_id         = github_enterprise_team.test.team_id
  depends_on      = [github_enterprise_team_organizations.test]
}`, teamCfg, enterprise, orgs, enterprise)
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories, CheckDestroy: checkTeamGone(c, enterprise, created), Steps: []resource.TestStep{
		{Config: teamCfg, Check: resource.ComposeTestCheckFunc(captureTeam(created), checkNewTeamID(created, baseline))},
		{Config: cfg(`["` + orgA + `"]`), Check: resource.ComposeTestCheckFunc(
			checkOrgSlugs("github_enterprise_team_organizations.test", orgA),
			checkOrgSlugs("data.github_enterprise_team_organizations.test", orgA),
		)},
		{ResourceName: "github_enterprise_team_organizations.test", ImportState: true, ImportStateVerify: true,
			ImportStateIdFunc: func(_ *terraform.State) (string, error) {
				return fmt.Sprintf("%s/%d", enterprise, created.id), nil
			}},
		// Drift: an out-of-band removal is reconciled back to the configured set.
		{Config: cfg(`["` + orgA + `"]`), PreConfig: func() {
			if err := c.ChangeOrganizations(context.Background(), enterprise, created.id, "remove", []string{orgA}); err != nil {
				t.Errorf("out-of-band organization removal failed: %v", err)
			}
		}, Check: checkOrgSlugs("github_enterprise_team_organizations.test", orgA)},
		{Config: cfg(`["` + orgA + `", "` + orgB + `"]`), Check: checkOrgSlugs("github_enterprise_team_organizations.test", orgA, orgB)},
		// Authoritative set semantics: dropping orgB removes it.
		{Config: cfg(`["` + orgA + `"]`), Check: checkOrgSlugs("github_enterprise_team_organizations.test", orgA)},
		// Authoritative reconciliation of an out-of-band addition: orgB is
		// added outside Terraform and removed again by the configured [orgA].
		{Config: cfg(`["` + orgA + `"]`), PreConfig: func() {
			if err := c.ChangeOrganizations(context.Background(), enterprise, created.id, "add", []string{orgB}); err != nil {
				t.Errorf("out-of-band organization addition failed: %v", err)
			}
		}, Check: resource.ComposeTestCheckFunc(
			checkOrgSlugs("github_enterprise_team_organizations.test", orgA),
			checkOrgSlugsRemote(c, enterprise, created, orgA),
		)},
		// The empty configured set and its data source both report no
		// assignments; the final deletion check verifies remote emptiness too.
		{Config: cfg(`[]`), Check: resource.ComposeTestCheckFunc(
			resource.TestCheckResourceAttr("github_enterprise_team_organizations.test", "organization_slugs.#", "0"),
			checkOrgSlugs("data.github_enterprise_team_organizations.test"),
		)},
		// Deleting the assignment resource removes remaining assignments while
		// the team itself remains.
		{Config: teamCfg, Check: resource.ComposeTestCheckFunc(
			checkOrgSlugsRemote(c, enterprise, created),
			checkTeamExists(c, enterprise, created),
		)},
	}})
}
