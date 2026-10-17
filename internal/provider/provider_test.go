package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

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
	if len(r.ResourceSchemas) != 3 || len(r.DataSourceSchemas) != 4 {
		t.Fatalf("resources=%d data sources=%d", len(r.ResourceSchemas), len(r.DataSourceSchemas))
	}
}

// This HTTP fixture exercises the provider through the Terraform CLI and plugin
// protocol, not just individual CRUD methods. It intentionally requires no
// enterprise credentials and never calls api.github.com.
type enterpriseAPI struct {
	mu                                      sync.Mutex
	team                                    *client.Team
	members                                 map[string]bool
	orgs                                    map[string]bool
	creates, memberWrites, assignmentWrites int
	deny                                    bool
	failRemoval                             bool
}

func newAPI(t *testing.T) (*enterpriseAPI, string) {
	t.Helper()
	a := &enterpriseAPI{members: map[string]bool{}, orgs: map[string]bool{}}
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
	path := strings.TrimPrefix(r.URL.Path, "/enterprises/acme/teams")
	if path == r.URL.Path {
		http.NotFound(w, r)
		return
	}
	if path == "" {
		if r.Method == "GET" {
			teams := []client.Team{}
			if a.team != nil {
				teams = append(teams, *a.team)
			}
			_ = json.NewEncoder(w).Encode(teams)
			return
		}
		if r.Method == "POST" {
			a.creates++
			a.team = &client.Team{ID: 42}
			a.updateTeam(r)
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(a.team)
			return
		}
	}
	if a.team == nil {
		http.NotFound(w, r)
		return
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if parts[0] != "42" && parts[0] != a.team.Slug {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 1 {
		switch r.Method {
		case "GET":
			_ = json.NewEncoder(w).Encode(a.team)
		case "PATCH":
			a.updateTeam(r)
			_ = json.NewEncoder(w).Encode(a.team)
		case "DELETE":
			a.team = nil
			a.members = map[string]bool{}
			a.orgs = map[string]bool{}
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
			if a.team.GroupID != "" {
				http.Error(w, "IdP membership", 422)
				return
			}
			a.members[user] = true
			a.memberWrites++
			w.WriteHeader(201)
		case "GET":
			if !a.members[user] {
				http.NotFound(w, r)
				return
			}
		case "DELETE":
			delete(a.members, user)
			a.memberWrites++
			w.WriteHeader(204)
			return
		default:
			http.Error(w, "method", 405)
			return
		}
		_ = json.NewEncoder(w).Encode(client.User{Login: user, ID: 7})
		return
	}
	if parts[1] == "organizations" {
		if len(parts) == 3 && r.Method == "POST" {
			if a.team.OrganizationSelectionType == nil || *a.team.OrganizationSelectionType != "selected" {
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
					a.orgs[s] = true
				} else {
					delete(a.orgs, s)
				}
			}
			a.assignmentWrites++
			if parts[2] == "remove" {
				w.WriteHeader(204)
				return
			}
		}
		orgs := []client.Organization{}
		names := []string{}
		for s := range a.orgs {
			names = append(names, s)
		}
		sort.Strings(names)
		for _, s := range names {
			orgs = append(orgs, client.Organization{Login: s})
		}
		_ = json.NewEncoder(w).Encode(orgs)
		return
	}
	http.NotFound(w, r)
}
func (a *enterpriseAPI) updateTeam(r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	if v, ok := body["name"].(string); ok {
		a.team.Name = v
		a.team.Slug = "ent:" + strings.ReplaceAll(strings.ToLower(v), " ", "-")
	}
	if v, ok := body["description"].(string); ok {
		a.team.Description = v
	}
	if v, ok := body["organization_selection_type"].(string); ok {
		a.team.OrganizationSelectionType = new(v)
	}
	if v, ok := body["notification_setting"].(string); ok {
		a.team.NotificationSetting = new(v)
	}
	if v, ok := body["group_id"]; ok {
		a.team.GroupID = ""
		if s, ok := v.(string); ok {
			a.team.GroupID = s
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
		if api.team != nil || len(api.members) != 0 || len(api.orgs) != 0 {
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
			api.orgs["external-org"] = true
			delete(api.members, "octocat")
		}, Check: resource.TestCheckResourceAttr("github_enterprise_team_organizations.test", "organization_slugs.#", "1")},
		{Config: config(base, "New Platform", "", "", `[]`), Check: resource.TestCheckResourceAttr("github_enterprise_team_organizations.test", "organization_slugs.#", "0")},
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
			api.team.Name = "Renamed elsewhere"
			api.team.Slug = "ent:renamed-elsewhere"
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
			if api.creates != 1 || api.team.GroupID != "" {
				return fmt.Errorf("group was not unlinked in place")
			}
			return nil
		}},
	}})
}

func TestTerraformRefusesToOverwriteOrganizations(t *testing.T) {
	requireTerraform(t)
	api, base := newAPI(t)
	api.team = &client.Team{ID: 42, Name: "Platform", Slug: "ent:platform", OrganizationSelectionType: new("selected"), NotificationSetting: new("notifications_enabled")}
	api.orgs["existing-org"] = true
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
	if api.assignmentWrites != 0 || !api.orgs["existing-org"] {
		t.Fatal("existing assignments changed")
	}
}

func TestImportValidation(t *testing.T) {
	for _, id := range []string{"/42", "acme/0", "acme/-1", "acme/01", "acme/9223372036854775808", "acme/ent:platform", "acme/42/extra"} {
		if _, err := parseImportID(id, 2); err == nil {
			t.Errorf("accepted %q", id)
		}
	}
	for _, id := range []string{"acme/42/", "acme/42/user/path", "acme/42/.."} {
		if _, err := parseImportID(id, 3); err == nil {
			t.Errorf("accepted %q", id)
		}
	}
}

// Real enterprise acceptance tests are opt-in and use an isolated team. They
// never discover or reuse an existing team. The test runner destroys it.
func TestAccTeam(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Set TF_ACC=1 to use a real GitHub Enterprise test account")
	}
	enterprise := os.Getenv("GITHUB_ENTERPRISE_SLUG")
	if enterprise == "" {
		t.Fatal("GITHUB_ENTERPRISE_SLUG is required")
	}
	name := "tf-acc-" + acctest.RandStringFromCharSet(10, acctest.CharSetAlphaNum)
	cfg := func(n, desc string) string {
		return fmt.Sprintf(`provider "github-enterprise" {}
resource "github_enterprise_team" "test" {
  provider = github-enterprise

 enterprise_slug=%q
name=%q
description=%q
}`, enterprise, n, desc)
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories, Steps: []resource.TestStep{
		{Config: cfg(name, "Acceptance test")},
		{ResourceName: "github_enterprise_team.test", ImportState: true, ImportStateVerify: true},
		{Config: cfg(name+"-renamed", "")},
	}})
}
