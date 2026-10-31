package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/go-github/v93/github"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/vmvarela/terraform-provider-github-enterprise/internal/client"
)

// Credential-free fixture. Identifier shapes below are invented for the fixture
// and are NOT verified against live GitHub; only the documented resource types
// (User, Org, Repo, Team) are used.
type ccRec struct {
	id, name, state string
	azure           *string
	res             []github.CostCenterResource
}
type ccAPI struct {
	mu      sync.Mutex
	centers map[string]*ccRec
	order   []string
	next    int
	deny    int    // non-zero: every request fails with this status
	failOn  string // "METHOD suffix" (e.g. "DELETE /resource"): fail with 500 before committing
	// failAfterCommit is like failOn but commits the change first.
	failAfterCommit string
	denyReadsAfter  bool // after any committed write, GET fails with 403
	readsDenied     bool
	onAdd           func(a *ccAPI) // runs before an add commits (simulates a concurrent change)
	writes          []string
	detailStatus    map[string]int // GET /{id} fails with this status
	keepOnDelete    bool           // DELETE /resource reports success but changes nothing
	summarize       bool           // list responses omit resources
	creates         int
}

var typeOfField = map[string]string{"users": "User", "organizations": "Org", "repositories": "Repo", "enterprise_teams": "Team"}

func newCCAPI(t *testing.T) (*ccAPI, string) {
	t.Helper()
	a := &ccAPI{centers: map[string]*ccRec{}}
	s := httptest.NewServer(http.HandlerFunc(a.serve))
	t.Cleanup(s.Close)
	return a, s.URL
}
func (a *ccAPI) seed(id, name string, res ...github.CostCenterResource) *ccRec {
	c := &ccRec{id: id, name: name, state: "active", res: res}
	a.centers[id] = c
	a.order = append(a.order, id)
	return c
}
func res(typ, name string) github.CostCenterResource {
	return github.CostCenterResource{Type: typ, Name: name}
}
func (a *ccAPI) held(id string) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for _, r := range a.centers[id].res {
		out = append(out, r.Type+":"+r.Name)
	}
	return out
}
func (a *ccAPI) writeLog() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.writes...)
}
func (c *ccRec) json(page, per int) map[string]any {
	lo, hi := (page-1)*per, page*per
	if lo > len(c.res) {
		lo = len(c.res)
	}
	if hi > len(c.res) {
		hi = len(c.res)
	}
	items := []map[string]string{}
	for _, r := range c.res[lo:hi] {
		items = append(items, map[string]string{"type": r.Type, "name": r.Name})
	}
	m := map[string]any{"id": c.id, "name": c.name, "resources": items, "has_next_page": hi < len(c.res)}
	if c.state != "" {
		m["state"] = c.state
	}
	if c.azure != nil {
		m["azure_subscription"] = *c.azure
	}
	return m
}
func (a *ccAPI) heldElsewhere(self, typ, name string) (string, int) {
	for _, id := range a.order {
		c := a.centers[id]
		if id == self || c.state == "deleted" {
			continue
		}
		for i, r := range c.res {
			if r.Type == typ && strings.EqualFold(r.Name, name) {
				return id, i
			}
		}
	}
	return "", 0
}

func (a *ccAPI) serve(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	reply := func(status int, v any) { w.WriteHeader(status); _ = json.NewEncoder(w).Encode(v) }
	if r.Header.Get("Authorization") != "Bearer mock-token" {
		reply(401, map[string]string{})
		return
	}
	if a.deny != 0 || (a.readsDenied && r.Method == http.MethodGet) {
		status := a.deny
		if status == 0 {
			status = 403
		}
		reply(status, map[string]string{})
		return
	}
	segs := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(segs) < 5 || segs[0] != "enterprises" || segs[2] != "settings" || segs[3] != "billing" || segs[4] != "cost-centers" {
		reply(404, map[string]string{})
		return
	}
	enterprise := segs[1]
	if enterprise != "acme" && enterprise != "other" {
		reply(404, map[string]string{})
		return
	}
	tail := segs[5:]
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	if r.Method != http.MethodGet {
		entry := r.Method + " " + strings.Join(tail, "/")
		if len(tail) == 2 {
			entry = r.Method + " " + tail[0] + " resource " + canonicalBody(body)
		}
		a.writes = append(a.writes, entry)
	}
	switch {
	case len(tail) == 0 && r.Method == http.MethodGet:
		out := []map[string]any{}
		for _, id := range a.order {
			c := a.centers[id]
			if s := r.URL.Query().Get("state"); s != "" && c.state != s {
				continue
			}
			j := c.json(1, 1<<20)
			if a.summarize {
				j["resources"] = []map[string]string{}
			}
			out = append(out, j)
		}
		reply(200, map[string]any{"costCenters": out})
	case len(tail) == 0 && r.Method == http.MethodPost:
		a.next++
		a.creates++
		c := a.seed("cc-"+strconv.Itoa(1000+a.next), body["name"].(string))
		reply(200, c.json(1, 100))
	case len(tail) == 1 && r.Method == http.MethodGet && a.detailStatus[tail[0]] != 0:
		reply(a.detailStatus[tail[0]], map[string]string{})
	case len(tail) >= 1 && a.centers[tail[0]] == nil:
		reply(404, map[string]string{})
	case len(tail) == 1 && r.Method == http.MethodGet:
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		reply(200, a.centers[tail[0]].json(max(page, 1), 2)) // small pages exercise multi-page reads
	case len(tail) == 1 && r.Method == http.MethodPatch:
		a.centers[tail[0]].name = body["name"].(string)
		reply(200, a.centers[tail[0]].json(1, 100))
	case len(tail) == 1 && r.Method == http.MethodDelete:
		a.centers[tail[0]].state = "deleted"
		reply(200, map[string]string{"id": tail[0], "costCenterState": "CostCenterArchived"})
	case len(tail) == 2 && tail[1] == "resource":
		a.changeResources(w, r, a.centers[tail[0]], body)
	default:
		reply(404, map[string]string{})
	}
}
func canonicalBody(b map[string]any) string {
	out, _ := json.Marshal(b) // map keys marshal sorted
	return string(out)
}
func (a *ccAPI) changeResources(w http.ResponseWriter, r *http.Request, c *ccRec, body map[string]any) {
	key := r.Method + " /resource"
	if a.failOn == key+"-404" && r.Method == http.MethodDelete {
		w.WriteHeader(404)
		return
	}
	if a.keepOnDelete && r.Method == http.MethodDelete {
		_ = json.NewEncoder(w).Encode(map[string]any{"message": "ok"})
		return
	}
	if a.failOn == key {
		w.WriteHeader(500)
		return
	}
	if r.Method == http.MethodPost && a.onAdd != nil {
		a.onAdd(a)
	}
	moved := []map[string]string{}
	for field, typ := range typeOfField {
		items, _ := body[field].([]any)
		for _, it := range items {
			name := it.(string)
			if r.Method == http.MethodPost {
				if other, i := a.heldElsewhere(c.id, typ, name); other != "" {
					o := a.centers[other]
					o.res = append(o.res[:i], o.res[i+1:]...)
					moved = append(moved, map[string]string{"resource_type": typ, "name": name, "previous_cost_center": other})
				}
				c.res = append(c.res, res(typ, name))
				continue
			}
			for i, x := range c.res {
				if x.Type == typ && x.Name == name {
					c.res = append(c.res[:i], c.res[i+1:]...)
					break
				}
			}
		}
	}
	if a.failAfterCommit == key {
		w.WriteHeader(500)
		return
	}
	if a.denyReadsAfter {
		a.readsDenied = true
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"message": "ok", "reassigned_resources": moved})
}

func ccProvider(base string) string {
	return fmt.Sprintf("provider \"github-enterprise\" {\n token = \"mock-token\"\n base_url = %q\n enterprise_slug = \"acme\"\n}\n", base)
}

const ccBlock = `
resource "github_enterprise_cost_center" "test" {
  provider = github-enterprise
  name     = %q
}
`

func ccAssignments(center string, sets map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"github_enterprise_cost_center_resources\" \"test\" {\n provider = github-enterprise\n cost_center_id = %s\n", center)
	for k, v := range sets {
		fmt.Fprintf(&b, " %s = %s\n", k, v)
	}
	b.WriteString("}\n")
	return b.String()
}

func TestTerraformCostCenterLifecycle(t *testing.T) {
	requireTerraform(t)
	api, base := newCCAPI(t)
	api.seed("legacy", "Legacy", res("User", "u1"), res("Org", "o1"), res("Repo", "r1"), res("Team", "t1"), res("Widget", "w1"))
	data := `
data "github_enterprise_cost_center" "test" {
  provider = github-enterprise
  cost_center_id = "legacy"
}
data "github_enterprise_cost_centers" "test" {
  provider = github-enterprise
  depends_on = [github_enterprise_cost_center.test]
}
data "github_enterprise_cost_centers" "archived" {
  provider = github-enterprise
  state = "deleted"
  depends_on = [github_enterprise_cost_center.test]
}
`
	cfg := func(name string) string { return ccProvider(base) + fmt.Sprintf(ccBlock, name) + data }
	// Exercise the live guard's delegated lifecycle and import without real credentials.
	tfresource.UnitTest(t, tfresource.TestCase{ProtoV6ProviderFactories: guardedFactories(map[string]bool{"legacy": true}), CheckDestroy: func(*terraform.State) error {
		api.mu.Lock()
		defer api.mu.Unlock()
		for _, c := range api.centers {
			if c.id != "legacy" && c.state != "deleted" {
				return fmt.Errorf("cost center %s was not archived", c.id)
			}
		}
		return nil
	}, Steps: []tfresource.TestStep{
		{Config: cfg("Platform"), Check: tfresource.ComposeTestCheckFunc(
			tfresource.TestCheckResourceAttr("github_enterprise_cost_center.test", "cost_center_id", "cc-1001"),
			tfresource.TestCheckResourceAttr("github_enterprise_cost_center.test", "id", "acme:cc-1001"),
			tfresource.TestCheckResourceAttr("data.github_enterprise_cost_center.test", "resources.#", "5"), // five raw items over three read pages
			tfresource.TestCheckResourceAttr("data.github_enterprise_cost_center.test", "resources.4.type", "Widget"),
			tfresource.TestCheckResourceAttr("data.github_enterprise_cost_centers.test", "cost_centers.#", "2"),
			tfresource.TestCheckResourceAttr("data.github_enterprise_cost_centers.test", "state", "active"),
			tfresource.TestCheckResourceAttr("data.github_enterprise_cost_centers.archived", "cost_centers.#", "0"),
		)},
		{ResourceName: "github_enterprise_cost_center.test", ImportState: true, ImportStateVerify: true},
		{Config: cfg("Platform Renamed"), Check: tfresource.ComposeTestCheckFunc(
			tfresource.TestCheckResourceAttr("github_enterprise_cost_center.test", "cost_center_id", "cc-1001"),
			tfresource.TestCheckResourceAttr("github_enterprise_cost_center.test", "name", "Platform Renamed"),
			func(*terraform.State) error {
				api.mu.Lock()
				defer api.mu.Unlock()
				if api.creates != 1 {
					return fmt.Errorf("rename recreated the cost center: creates=%d", api.creates)
				}
				return nil
			})},
		// Out-of-band archive: refresh drops the resource and a new center is created.
		{Config: cfg("Platform Renamed"), PreConfig: func() {
			api.mu.Lock()
			api.centers["cc-1001"].state = "deleted"
			api.mu.Unlock()
		}, Check: tfresource.TestCheckResourceAttr("github_enterprise_cost_center.test", "cost_center_id", "cc-1002")},
	}})
}

func TestTerraformCostCenterInheritedSlugReplaces(t *testing.T) {
	requireTerraform(t)
	api, base := newCCAPI(t)
	cfg := func(slug string) string {
		return fmt.Sprintf("provider \"github-enterprise\" {\n token = \"mock-token\"\n base_url = %q\n enterprise_slug = %q\n}\n", base, slug) + fmt.Sprintf(ccBlock, "Platform")
	}
	tfresource.UnitTest(t, tfresource.TestCase{ProtoV6ProviderFactories: factories, Steps: []tfresource.TestStep{
		{Config: cfg("acme"), Check: tfresource.TestCheckResourceAttr("github_enterprise_cost_center.test", "enterprise_slug", "acme")},
		{Config: cfg("other"), Check: func(*terraform.State) error {
			api.mu.Lock()
			defer api.mu.Unlock()
			// The first center must be archived in the OLD enterprise, then recreated.
			if api.centers["cc-1001"].state != "deleted" || api.creates != 2 {
				return fmt.Errorf("replacement did not archive the old center: %+v creates=%d", api.centers["cc-1001"], api.creates)
			}
			return nil
		}},
	}})
}

func TestTerraformCostCenterResourcesLifecycle(t *testing.T) {
	requireTerraform(t)
	api, base := newCCAPI(t)
	api.seed("elsewhere", "Elsewhere", res("User", "keep-me"), res("Org", "keep-org"))
	both := func(sets map[string]string) string {
		return ccProvider(base) + fmt.Sprintf(ccBlock, "Platform") + ccAssignments("github_enterprise_cost_center.test.cost_center_id", sets)
	}
	initial := map[string]string{"users": `["alice","bob","carol"]`, "organizations": `["org-a"]`, "repositories": `["octo/repo"]`, "enterprise_teams": `["team-a"]`}
	const addr = "github_enterprise_cost_center_resources.test"
	tfresource.UnitTest(t, tfresource.TestCase{ProtoV6ProviderFactories: factories, CheckDestroy: func(*terraform.State) error {
		if got := api.held("elsewhere"); len(got) != 2 {
			return fmt.Errorf("destroy touched another cost center: %v", got)
		}
		if got := api.held("cc-1001"); len(got) != 0 {
			return fmt.Errorf("destroy left assignments: %v", got)
		}
		return nil
	}, Steps: []tfresource.TestStep{
		{Config: both(initial), Check: tfresource.ComposeTestCheckFunc(
			tfresource.TestCheckResourceAttr(addr, "users.#", "3"), // three users across two read pages
			func(*terraform.State) error {
				w := api.writeLog()
				want := `POST cc-1001 resource {"enterprise_teams":["team-a"],"organizations":["org-a"],"repositories":["octo/repo"],"users":["alice","bob","carol"]}`
				if len(w) != 2 || w[1] != want { // w[0] is the cost center create
					return fmt.Errorf("writes: %v", w)
				}
				return nil
			})},
		{ResourceName: addr, ImportState: true, ImportStateVerify: true},
		// Update: remove delta first, then add delta, one request each.
		{Config: both(map[string]string{"users": `["alice","dave"]`, "organizations": `["org-a"]`, "repositories": `["octo/repo"]`, "enterprise_teams": `["team-a"]`}), Check: func(*terraform.State) error {
			w := api.writeLog()[2:]
			if len(w) != 2 || w[0] != `DELETE cc-1001 resource {"users":["bob","carol"]}` || w[1] != `POST cc-1001 resource {"users":["dave"]}` {
				return fmt.Errorf("update writes: %v", w)
			}
			return nil
		}},
		// Out-of-band addition is reconciled away; a no-op apply writes nothing.
		{Config: both(map[string]string{"users": `["alice","dave"]`, "organizations": `["org-a"]`, "repositories": `["octo/repo"]`, "enterprise_teams": `["team-a"]`}), PreConfig: func() {
			api.mu.Lock()
			api.centers["cc-1001"].res = append(api.centers["cc-1001"].res, res("User", "intruder"))
			api.mu.Unlock()
		}, Check: func(*terraform.State) error {
			if got := api.writeLog()[4:]; len(got) != 1 || got[0] != `DELETE cc-1001 resource {"users":["intruder"]}` {
				return fmt.Errorf("drift writes: %v", got)
			}
			return nil
		}},
		// Omitted arguments mean none.
		{Config: both(map[string]string{"users": `["alice"]`}), Check: tfresource.TestCheckResourceAttr(addr, "organizations.#", "0")},
	}})
}

func TestTerraformCostCenterResourcesRefuseUnsafeOwnership(t *testing.T) {
	requireTerraform(t)
	for name, tc := range map[string]struct {
		seed   func(*ccAPI)
		sets   map[string]string
		expect string
	}{
		"adoption":       {func(a *ccAPI) { a.seed("mine", "Mine", res("User", "existing")) }, map[string]string{"users": `["x"]`}, "Import this cost center"},
		"cross center":   {func(a *ccAPI) { a.seed("mine", "Mine"); a.seed("theirs", "Theirs", res("Org", "Shared-Org")) }, map[string]string{"organizations": `["shared-org"]`}, "already assigned to cost center theirs"},
		"unsupported":    {func(a *ccAPI) { a.seed("mine", "Mine", res("Widget", "w")) }, map[string]string{"users": `["x"]`}, "unsupported type"},
		"case only dups": {func(a *ccAPI) { a.seed("mine", "Mine") }, map[string]string{"users": `["Bob","bob"]`}, "unique ignoring case"},
	} {
		t.Run(name, func(t *testing.T) {
			api, base := newCCAPI(t)
			tc.seed(api)
			tfresource.UnitTest(t, tfresource.TestCase{ProtoV6ProviderFactories: factories, Steps: []tfresource.TestStep{
				{Config: ccProvider(base) + ccAssignments(`"mine"`, tc.sets), ExpectError: regexp.MustCompile(tc.expect)},
			}})
			if w := api.writeLog(); len(w) != 0 {
				t.Fatalf("writes before refusing: %v", w)
			}
		})
	}
}

func TestTerraformCostCenterResourcesUpdateConflictDoesNotRemove(t *testing.T) {
	requireTerraform(t)
	api, base := newCCAPI(t)
	api.seed("mine", "Mine")
	api.seed("theirs", "Theirs", res("User", "taken"))
	cfg := func(users string) string {
		return ccProvider(base) + ccAssignments(`"mine"`, map[string]string{"users": users})
	}
	tfresource.UnitTest(t, tfresource.TestCase{ProtoV6ProviderFactories: factories, Steps: []tfresource.TestStep{
		{Config: cfg(`["a"]`)},
		{Config: cfg(`["taken"]`), ExpectError: regexp.MustCompile("already assigned")},
		{Config: cfg(`["a"]`), Check: func(*terraform.State) error { // still intact, no removal was attempted
			if got := api.held("mine"); len(got) != 1 || got[0] != "User:a" || len(api.writeLog()) != 1 {
				return fmt.Errorf("held=%v writes=%v", got, api.writeLog())
			}
			return nil
		}},
	}})
}

// Direct resource-method tests for partial-failure and permission semantics.
func ccAssignState(t *testing.T, r *costCenterResourcesResource, users ...string) (tfsdk.State, costCenterResourcesModel) {
	t.Helper()
	ctx := context.Background()
	var sr resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sr)
	m := costCenterResourcesModel{ID: types.StringValue("acme:mine"), Enterprise: types.StringValue("acme"), CostCenterID: types.StringValue("mine")}
	if ds := m.setObserved(ctx, client.CostCenterAssignments{Users: users}); ds.HasError() {
		t.Fatal(ds)
	}
	s := tfsdk.State{Schema: sr.Schema}
	if ds := s.Set(ctx, &m); ds.HasError() {
		t.Fatal(ds)
	}
	return s, m
}
func ccResourceFor(t *testing.T, base string) *costCenterResourcesResource {
	c, err := client.New(base, "mock-token", client.DefaultAPIVersion)
	if err != nil {
		t.Fatal(err)
	}
	return &costCenterResourcesResource{configuredResource{client: c}}
}
func stateUsers(t *testing.T, s tfsdk.State) []string {
	var m costCenterResourcesModel
	if ds := s.Get(context.Background(), &m); ds.HasError() {
		t.Fatal(ds)
	}
	var u []string
	m.Users.ElementsAs(context.Background(), &u, false)
	return u
}
func updateTo(t *testing.T, r *costCenterResourcesResource, old tfsdk.State, om costCenterResourcesModel, users ...string) *resource.UpdateResponse {
	ctx := context.Background()
	planned := om
	planned.Users, _ = types.SetValueFrom(ctx, types.StringType, users)
	ps := tfsdk.State{Schema: old.Schema}
	ps.Set(ctx, &planned)
	resp := &resource.UpdateResponse{State: old}
	r.Update(ctx, resource.UpdateRequest{Plan: tfsdk.Plan{Schema: old.Schema, Raw: ps.Raw}, State: old}, resp)
	return resp
}

func TestCostCenterResourcesPartialFailuresKeepActualState(t *testing.T) {
	for name, tc := range map[string]struct {
		setup     func(*ccAPI)
		wantUsers []string
		wantState bool // true: state is rewritten with observed membership
		wantLog   int
	}{
		"remove fails before commit": {func(a *ccAPI) { a.failOn = "DELETE /resource" }, []string{"old"}, true, 1},
		"add fails after commit":     {func(a *ccAPI) { a.failAfterCommit = "POST /resource" }, []string{"new"}, true, 2},
		"refresh fails after write":  {func(a *ccAPI) { a.denyReadsAfter = true }, []string{"old"}, false, 2},
		"unexpected reassignment": {func(a *ccAPI) {
			a.onAdd = func(a *ccAPI) { a.seed("late", "Late", res("User", "new")) }
		}, []string{"new"}, true, 2},
	} {
		t.Run(name, func(t *testing.T) {
			api, base := newCCAPI(t)
			api.seed("mine", "Mine", res("User", "old"))
			tc.setup(api)
			r := ccResourceFor(t, base)
			st, m := ccAssignState(t, r, "old")
			resp := updateTo(t, r, st, m, "new")
			if !resp.Diagnostics.HasError() {
				t.Fatal("expected an error")
			}
			if got := stateUsers(t, resp.State); len(got) != 1 || got[0] != tc.wantUsers[0] {
				t.Fatalf("state users %v, want %v: %v", got, tc.wantUsers, resp.Diagnostics)
			}
			if resp.State.Raw.IsNull() {
				t.Fatal("resource identity lost")
			}
			if len(api.writeLog()) != tc.wantLog {
				t.Fatalf("writes %v", api.writeLog())
			}
		})
	}
}

func TestCostCenterReadSemantics(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		setup   func(*ccAPI)
		removed bool
		failed  bool
	}{
		"missing":          {func(a *ccAPI) {}, true, false},
		"archived":         {func(a *ccAPI) { a.seed("mine", "Mine").state = "deleted" }, true, false},
		"forbidden":        {func(a *ccAPI) { a.seed("mine", "Mine"); a.deny = 403 }, false, true},
		"rate limited":     {func(a *ccAPI) { a.seed("mine", "Mine"); a.deny = 429 }, false, true},
		"server error":     {func(a *ccAPI) { a.seed("mine", "Mine"); a.deny = 502 }, false, true},
		"unsupported type": {func(a *ccAPI) { a.seed("mine", "Mine", res("Widget", "x")) }, false, true},
	} {
		t.Run(name, func(t *testing.T) {
			api, base := newCCAPI(t)
			tc.setup(api)
			r := ccResourceFor(t, base)
			st, _ := ccAssignState(t, r, "old")
			resp := resource.ReadResponse{State: st}
			r.Read(ctx, resource.ReadRequest{State: st}, &resp)
			if resp.State.Raw.IsNull() != tc.removed || resp.Diagnostics.HasError() != tc.failed {
				t.Fatalf("removed=%v diagnostics=%v", resp.State.Raw.IsNull(), resp.Diagnostics)
			}
			if tc.failed && !resp.State.Raw.Equal(st.Raw) {
				t.Fatal("state modified by a failed read")
			}
		})
	}
}

func TestCostCenterResourcesDeleteRemovesOnlyOwnSet(t *testing.T) {
	api, base := newCCAPI(t)
	api.seed("mine", "Mine", res("User", "a"), res("Repo", "o/r"))
	api.seed("other", "Other", res("User", "b"))
	r := ccResourceFor(t, base)
	st, _ := ccAssignState(t, r, "a")
	resp := resource.DeleteResponse{State: st}
	r.Delete(context.Background(), resource.DeleteRequest{State: st}, &resp)
	if resp.Diagnostics.HasError() || len(api.held("mine")) != 0 || len(api.held("other")) != 1 {
		t.Fatalf("%v mine=%v other=%v", resp.Diagnostics, api.held("mine"), api.held("other"))
	}
	if w := api.writeLog(); len(w) != 1 || w[0] != `DELETE mine resource {"repositories":["o/r"],"users":["a"]}` {
		t.Fatalf("writes %v", w)
	}
	// Already-archived parent is treated as absent without writes.
	api.centers["mine"].state = "deleted"
	r.Delete(context.Background(), resource.DeleteRequest{State: st}, &resp)
	if resp.Diagnostics.HasError() || len(api.writeLog()) != 1 {
		t.Fatalf("archived delete wrote or failed: %v", resp.Diagnostics)
	}
}

func TestCostCenterCreateKeepsIDWhenResponseIncomplete(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"id":"cc-77","resources":[]}`) }))
	defer srv.Close()
	c, err := client.New(srv.URL, "mock-token", client.DefaultAPIVersion)
	if err != nil {
		t.Fatal(err)
	}
	r := &costCenterResource{configuredResource{client: c}}
	var sr resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sr)
	m := costCenterModel{Enterprise: types.StringValue("acme"), Name: types.StringValue("Planned"), ID: types.StringUnknown(), CostCenterID: types.StringUnknown(), State: types.StringUnknown(), Azure: types.StringUnknown()}
	ps := tfsdk.State{Schema: sr.Schema}
	ps.Set(ctx, &m)
	resp := resource.CreateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: ps.Raw}}
	r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: sr.Schema, Raw: ps.Raw}}, &resp)
	var got costCenterModel
	resp.State.Get(ctx, &got)
	if !resp.Diagnostics.HasError() || got.CostCenterID.ValueString() != "cc-77" || got.ID.ValueString() != "acme:cc-77" || got.Name.ValueString() != "Planned" {
		t.Fatalf("ID not persisted: %v %+v", resp.Diagnostics, got)
	}
}

func TestCostCenterPlanChangeAndIDs(t *testing.T) {
	if _, _, err := planChange(client.CostCenterAssignments{Users: []string{"Bob"}}, client.CostCenterAssignments{Users: []string{"bob"}}); err == nil {
		t.Fatal("case-only rename must be refused, not removed and re-added")
	}
	add, remove, err := planChange(client.CostCenterAssignments{Users: []string{"a"}}, client.CostCenterAssignments{Users: []string{"a"}})
	if err != nil || !emptyAssignments(add) || !emptyAssignments(remove) {
		t.Fatalf("no-op delta: %v %v %v", add, remove, err)
	}
	for _, bad := range []string{"", ".", "..", "a/b", "a b", "a\nb", strings.Repeat("x", 257)} {
		if validCostCenterID(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, good := range []string{"42", "3f2c-uuid-like", "Opaque_ID"} {
		if !validCostCenterID(good) {
			t.Errorf("%q rejected", good)
		}
	}
	for _, bad := range []string{"acme", "acme:", ":x", "Acme:x", "acme:a:b", "acme:..", "acme/x"} {
		if _, _, err := parseCostCenterImportID(bad); err == nil {
			t.Errorf("import %q accepted", bad)
		}
	}
	if e, id, err := parseCostCenterImportID("acme:cc 1"); err == nil {
		t.Errorf("space accepted: %s %s", e, id)
	}
}

func TestCostCenterPreflightFailsClosedWithoutWrites(t *testing.T) {
	for name, setup := range map[string]func(*ccAPI){
		"unknown type": func(a *ccAPI) { a.seed("o", "O", res("Widget", "x")) },
		"blank name":   func(a *ccAPI) { a.seed("o", "O", res("User", " ")) },
		"detail 404":   func(a *ccAPI) { a.seed("o", "O"); a.detailStatus = map[string]int{"o": 404} },
		"detail 500":   func(a *ccAPI) { a.seed("o", "O"); a.detailStatus = map[string]int{"o": 500} },
	} {
		t.Run(name, func(t *testing.T) {
			api, base := newCCAPI(t)
			api.seed("mine", "Mine", res("User", "old"))
			setup(api)
			r := ccResourceFor(t, base)
			st, m := ccAssignState(t, r, "old")
			if resp := updateTo(t, r, st, m, "new"); !resp.Diagnostics.HasError() {
				t.Fatal("expected error")
			}
			if w := api.writeLog(); len(w) != 0 {
				t.Fatalf("writes %v", w)
			}
		})
	}
}

func TestCostCenterResourcesDeleteVerifies(t *testing.T) {
	for name, tc := range map[string]struct {
		setup     func(*ccAPI)
		wantErr   bool
		wantState []string // nil: state untouched
	}{
		"404 with active parent": {func(a *ccAPI) { a.failOn = "DELETE /resource-404" }, true, []string{"a"}},
		"partial commit failure": {func(a *ccAPI) { a.failAfterCommit = "DELETE /resource" }, true, []string{}},
		"200 but leftovers":      {func(a *ccAPI) { a.keepOnDelete = true }, true, []string{"a"}},
		"refresh fails":          {func(a *ccAPI) { a.denyReadsAfter = true }, true, nil},
	} {
		t.Run(name, func(t *testing.T) {
			api, base := newCCAPI(t)
			api.seed("mine", "Mine", res("User", "a"))
			tc.setup(api)
			r := ccResourceFor(t, base)
			st, _ := ccAssignState(t, r, "a")
			resp := resource.DeleteResponse{State: st}
			r.Delete(context.Background(), resource.DeleteRequest{State: st}, &resp)
			if resp.Diagnostics.HasError() != tc.wantErr {
				t.Fatalf("diagnostics %v", resp.Diagnostics)
			}
			if resp.State.Raw.IsNull() {
				t.Fatal("identity lost")
			}
			if tc.wantState != nil {
				if got := stateUsers(t, resp.State); len(got) != len(tc.wantState) {
					t.Fatalf("state %v want %v", got, tc.wantState)
				}
			}
		})
	}
}

func TestCostCentersDataSourceHydratesSummaries(t *testing.T) {
	api, base := newCCAPI(t)
	api.seed("c1", "One", res("User", "a"), res("User", "b"), res("Repo", "o/r"))
	api.summarize = true
	c, _ := client.New(base, "mock-token", client.DefaultAPIVersion)
	d := &costCentersDataSource{configuredDataSource{client: c}}
	ctx := context.Background()
	var sr datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &sr)
	cfg := costCentersDataModel{Enterprise: types.StringValue("acme"), State: types.StringNull(), ID: types.StringNull(), CostCenters: types.ListNull(types.ObjectType{AttrTypes: costCenterTypes})}
	s := tfsdk.State{Schema: sr.Schema}
	s.Set(ctx, &cfg)
	resp := datasource.ReadResponse{State: s}
	d.Read(ctx, datasource.ReadRequest{Config: tfsdk.Config{Schema: sr.Schema, Raw: s.Raw}}, &resp)
	var got costCentersDataModel
	resp.State.Get(ctx, &got)
	if resp.Diagnostics.HasError() || !strings.Contains(got.CostCenters.String(), `"o/r"`) || !strings.Contains(got.CostCenters.String(), `"b"`) {
		t.Fatalf("%v %s", resp.Diagnostics, got.CostCenters)
	}
	api.detailStatus = map[string]int{"c1": 500}
	resp = datasource.ReadResponse{State: s}
	d.Read(ctx, datasource.ReadRequest{Config: tfsdk.Config{Schema: sr.Schema, Raw: s.Raw}}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("incomplete hydration must fail")
	}
}

func TestCostCenterLiveGuardRefusesBaselineDelete(t *testing.T) {
	ctx := context.Background()
	api, base := newCCAPI(t)
	api.seed("old", "Old")
	api.seed("new", "New")
	c, _ := client.New(base, "mock-token", client.DefaultAPIVersion)
	var g resource.Resource
	for _, f := range (guardedProvider{New("test")().(*enterpriseProvider), map[string]bool{"old": true}}).Resources(ctx) {
		if r, ok := f().(guardedCostCenter); ok {
			r.client = c
			g = r
		}
	}
	if g == nil {
		t.Fatal("cost center resource not wrapped")
	}
	var sr resource.SchemaResponse
	g.Schema(ctx, resource.SchemaRequest{}, &sr)
	del := func(id string) resource.DeleteResponse {
		s := tfsdk.State{Schema: sr.Schema}
		s.Set(ctx, &costCenterModel{ID: types.StringValue("acme:" + id), Enterprise: types.StringValue("acme"), CostCenterID: types.StringValue(id), Name: types.StringValue("x"), State: types.StringNull(), Azure: types.StringNull()})
		resp := resource.DeleteResponse{State: s}
		g.Delete(ctx, resource.DeleteRequest{State: s}, &resp)
		return resp
	}
	for _, id := range []string{"old", ""} {
		if resp := del(id); !resp.Diagnostics.HasError() || len(api.writeLog()) != 0 {
			t.Fatalf("%q: %v writes=%v", id, resp.Diagnostics, api.writeLog())
		}
	}
	if resp := del("new"); resp.Diagnostics.HasError() || api.centers["new"].state != "deleted" || len(api.writeLog()) != 1 {
		t.Fatalf("delegate: %v %v", resp.Diagnostics, api.writeLog())
	}
}
