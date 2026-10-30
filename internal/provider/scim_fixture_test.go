package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

type scimAPI struct {
	mu     sync.Mutex
	writes int
}

// github SCIM returns at most one item per page here so the client must follow
// startIndex/totalResults instead of trusting a REST page link.
func scimPage(w http.ResponseWriter, r *http.Request, items []map[string]any) {
	filter := r.URL.Query().Get("filter")
	matched := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if scimMatches(filter, item) {
			matched = append(matched, item)
		}
	}
	start, _ := strconv.Atoi(r.URL.Query().Get("startIndex"))
	if start < 1 {
		start = 1
	}
	begin := start - 1
	if begin > len(matched) {
		begin = len(matched)
	}
	end := begin + 1
	if end > len(matched) {
		end = len(matched)
	}
	page := matched[begin:end]
	_ = json.NewEncoder(w).Encode(map[string]any{
		"schemas":      []string{"urn:ietf:params:scim:api:messages:2.0:ListResponse"},
		"totalResults": len(matched),
		"startIndex":   start,
		"itemsPerPage": len(page),
		"Resources":    page,
	})
}

func scimMatches(filter string, item map[string]any) bool {
	if filter == "" {
		return true
	}
	parts := strings.SplitN(filter, " eq ", 2)
	if len(parts) != 2 {
		return false
	}
	return fmt.Sprint(item[strings.TrimSpace(parts[0])]) == strings.Trim(strings.TrimSpace(parts[1]), `"`)
}

func scimObject(w http.ResponseWriter, r *http.Request, items []map[string]any) {
	id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	for _, item := range items {
		if fmt.Sprint(item["id"]) == id {
			_ = json.NewEncoder(w).Encode(item)
			return
		}
	}
	http.NotFound(w, r)
}

func (a *scimAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		a.mu.Lock()
		a.writes++
		a.mu.Unlock()
		http.Error(w, "read-only fixture", http.StatusMethodNotAllowed)
		return
	}
	groups := []map[string]any{
		{"id": "grp-9f8b", "externalId": "idp-group-1", "displayName": "Platform"},
		{"id": "grp-7c6d", "displayName": "Ops"},
		{"id": "grp-5b4c", "externalId": "idp-group-3"},
	}
	users := []map[string]any{
		{"id": "usr-4e5d", "externalId": "idp-user-1", "displayName": "Octo Cat", "userName": "octocat", "active": false},
		{"id": "usr-3d2c", "userName": "other", "active": true},
	}
	path := r.URL.Path
	switch {
	case strings.HasSuffix(path, "/Groups"):
		scimPage(w, r, groups)
	case strings.HasSuffix(path, "/Users"):
		scimPage(w, r, users)
	case strings.Contains(path, "/Groups/"):
		scimObject(w, r, groups)
	case strings.Contains(path, "/Users/"):
		scimObject(w, r, users)
	case strings.HasSuffix(path, "/teams/linked"):
		_, _ = fmt.Fprint(w, `{"id":42,"slug":"linked","name":"Linked","description":"","group_id":"grp-9f8b"}`)
	default:
		http.NotFound(w, r)
	}
}

func TestTerraformSCIM(t *testing.T) {
	requireTerraform(t)
	api := &scimAPI{}
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	cfg := fmt.Sprintf(`
provider "github-enterprise" {
  token           = "mock-token"
  base_url        = %q
  enterprise_slug = "acme"
}
data "github_enterprise_scim_group" "test" {
  provider      = github-enterprise
  scim_group_id = "grp-9f8b"
}
data "github_enterprise_scim_groups" "pages" {
  provider = github-enterprise
}
data "github_enterprise_scim_groups" "filtered" {
  provider = github-enterprise
  filter   = "id eq \"grp-7c6d\""
}
data "github_enterprise_scim_groups" "empty" {
  provider = github-enterprise
  filter   = "displayName eq \"Missing\""
}
data "github_enterprise_scim_user" "test" {
  provider     = github-enterprise
  scim_user_id = "usr-4e5d"
}
data "github_enterprise_scim_users" "filtered" {
  provider = github-enterprise
  filter   = "userName eq \"octocat\""
}
data "github_enterprise_team" "linked" {
  provider = github-enterprise
  slug     = "linked"
}
`, server.URL)
	checkNoWrites := func(*terraform.State) error {
		api.mu.Lock()
		defer api.mu.Unlock()
		if api.writes != 0 {
			return fmt.Errorf("read-only fixture received %d writes", api.writes)
		}
		return nil
	}
	tfresource.UnitTest(t, tfresource.TestCase{ProtoV6ProviderFactories: factories, CheckDestroy: checkNoWrites, Steps: []tfresource.TestStep{
		{Config: cfg, Check: tfresource.ComposeTestCheckFunc(
			checkNoWrites,
			tfresource.TestCheckResourceAttr("data.github_enterprise_scim_group.test", "id", "acme/grp-9f8b"),
			tfresource.TestCheckResourceAttr("data.github_enterprise_scim_group.test", "scim_group_id", "grp-9f8b"),
			tfresource.TestCheckResourceAttr("data.github_enterprise_scim_group.test", "external_id", "idp-group-1"),
			tfresource.TestCheckResourceAttr("data.github_enterprise_scim_group.test", "display_name", "Platform"),
			tfresource.TestCheckResourceAttr("data.github_enterprise_scim_groups.pages", "id", "acme/groups"),
			tfresource.TestCheckResourceAttr("data.github_enterprise_scim_groups.pages", "groups.#", "3"),
			tfresource.TestCheckResourceAttr("data.github_enterprise_scim_groups.pages", "groups.0.scim_group_id", "grp-9f8b"),
			tfresource.TestCheckResourceAttr("data.github_enterprise_scim_groups.pages", "groups.2.scim_group_id", "grp-5b4c"),
			tfresource.TestCheckResourceAttr("data.github_enterprise_scim_groups.pages", "groups.1.display_name", "Ops"),
			tfresource.TestCheckResourceAttr("data.github_enterprise_scim_groups.filtered", "groups.#", "1"),
			tfresource.TestCheckResourceAttr("data.github_enterprise_scim_groups.filtered", "groups.0.scim_group_id", "grp-7c6d"),
			tfresource.TestCheckResourceAttr("data.github_enterprise_scim_groups.empty", "groups.#", "0"),
			tfresource.TestCheckResourceAttr("data.github_enterprise_scim_user.test", "id", "acme/usr-4e5d"),
			tfresource.TestCheckResourceAttr("data.github_enterprise_scim_user.test", "scim_user_id", "usr-4e5d"),
			tfresource.TestCheckResourceAttr("data.github_enterprise_scim_user.test", "active", "false"),
			tfresource.TestCheckResourceAttr("data.github_enterprise_scim_user.test", "external_id", "idp-user-1"),
			tfresource.TestCheckResourceAttr("data.github_enterprise_scim_users.filtered", "users.#", "1"),
			tfresource.TestCheckResourceAttr("data.github_enterprise_scim_users.filtered", "users.0.scim_user_id", "usr-4e5d"),
			tfresource.TestCheckResourceAttr("data.github_enterprise_scim_users.filtered", "users.0.active", "false"),
			func(state *terraform.State) error {
				group, err := primaryState(state, "data.github_enterprise_scim_group.test")
				if err != nil {
					return err
				}
				team, err := primaryState(state, "data.github_enterprise_team.linked")
				if err != nil {
					return err
				}
				raw, linked := group.Attributes["scim_group_id"], team.Attributes["group_id"]
				if raw != "grp-9f8b" || linked != raw {
					return fmt.Errorf("raw scim_group_id %q is not the linked team group_id %q", raw, linked)
				}
				if group.Attributes["id"] == raw {
					return fmt.Errorf("the composite Terraform id must not be used as the raw group ID")
				}
				return nil
			},
		)},
	}})
}
