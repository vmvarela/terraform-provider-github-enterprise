package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// Credential-free fixture for the enterprise secret scanning custom-pattern
// endpoints. It stores exactly what a request carried, so an emptied list stays
// distinguishable from a never-sent one.
type spPattern struct {
	id                      int64
	name, pattern, slug     string
	state, version          string
	push                    bool
	start, end              *string
	mustMatch, mustNotMatch []string
}

func (p *spPattern) json() map[string]any {
	m := map[string]any{
		"id":                      p.id,
		"name":                    p.name,
		"pattern":                 p.pattern,
		"slug":                    p.slug,
		"state":                   p.state,
		"push_protection_enabled": p.push,
		"custom_pattern_version":  p.version,
	}
	if p.start != nil {
		m["start_delimiter"] = *p.start
	}
	if p.end != nil {
		m["end_delimiter"] = *p.end
	}
	if p.mustMatch != nil {
		m["must_match"] = p.mustMatch
	}
	if p.mustNotMatch != nil {
		m["must_not_match"] = p.mustNotMatch
	}
	return m
}

type spAPI struct {
	mu       sync.Mutex
	patterns map[int64]*spPattern
	next     int64
	rev      int
	creates  int
	deletes  int
	// lastPatch is the decoded PATCH body, so a test can assert which fields
	// were sent and that unconfigured ones were not.
	lastPatch map[string]any
	// failPatch makes the next PATCH answer with this status (a change that
	// lands after Terraform's refresh, which the provider cannot see).
	failPatch        int
	lastDeleteAction string
	lastDeleteID     int64
}

func newSPAPI(t *testing.T) (*spAPI, string) {
	t.Helper()
	a := &spAPI{patterns: map[int64]*spPattern{}}
	s := httptest.NewServer(http.HandlerFunc(a.serve))
	t.Cleanup(s.Close)
	return a, s.URL
}

func (a *spAPI) serve(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	reply := func(status int, v any) { w.WriteHeader(status); _ = json.NewEncoder(w).Encode(v) }
	if r.Header.Get("Authorization") != "Bearer mock-token" {
		reply(401, map[string]string{})
		return
	}
	segs := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(segs) < 4 || segs[0] != "enterprises" || segs[2] != "secret-scanning" || segs[3] != "custom-patterns" {
		reply(404, map[string]string{})
		return
	}
	tail := segs[4:]
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	switch {
	case len(tail) == 0 && r.Method == http.MethodPost:
		var req struct {
			Patterns []struct {
				Name           string   `json:"name"`
				Pattern        string   `json:"pattern"`
				StartDelimiter *string  `json:"start_delimiter"`
				EndDelimiter   *string  `json:"end_delimiter"`
				MustMatch      []string `json:"must_match"`
				MustNotMatch   []string `json:"must_not_match"`
			} `json:"patterns"`
		}
		_ = json.Unmarshal(mustMarshal(w, body), &req)
		if len(req.Patterns) != 1 {
			reply(422, map[string]string{"message": "expected exactly one pattern"})
			return
		}
		in := req.Patterns[0]
		if in.Name == "" || in.Pattern == "" {
			reply(422, map[string]string{"message": "name and pattern are required"})
			return
		}
		a.creates++
		a.rev++
		p := &spPattern{
			id: a.next + 1, name: in.Name, pattern: in.Pattern,
			slug:  strings.ReplaceAll(strings.ToLower(in.Name), " ", "-"),
			state: "unpublished", version: fmt.Sprintf("v%d", a.rev),
			mustMatch: in.MustMatch, mustNotMatch: in.MustNotMatch,
		}
		a.next = p.id
		// GitHub applies boundary defaults when the delimiters are omitted.
		if in.StartDelimiter == nil {
			s := "("
			p.start = &s
		} else {
			p.start = in.StartDelimiter
		}
		if in.EndDelimiter == nil {
			s := ")"
			p.end = &s
		} else {
			p.end = in.EndDelimiter
		}
		a.patterns[p.id] = p
		reply(201, map[string]any{"created_patterns": []any{p.json()}})
	case len(tail) == 0 && r.Method == http.MethodGet:
		out := []map[string]any{}
		for _, id := range sortedPatternIDs(a.patterns) {
			out = append(out, a.patterns[id].json())
		}
		reply(200, out)
	case len(tail) == 1 && r.Method == http.MethodPatch:
		id, ok := parseID(tail[0])
		p := a.patterns[id]
		if !ok || p == nil {
			reply(404, map[string]string{})
			return
		}
		if a.failPatch != 0 {
			reply(a.failPatch, map[string]string{"message": "pattern changed outside Terraform"})
			return
		}
		if body["custom_pattern_version"] != p.version {
			reply(412, map[string]string{"message": "stale custom_pattern_version"})
			return
		}
		a.lastPatch = body
		if v, ok := body["pattern"].(string); ok {
			p.pattern = v
		}
		applyDelimiter(body, "start_delimiter", &p.start)
		applyDelimiter(body, "end_delimiter", &p.end)
		applyList(body, "must_match", &p.mustMatch)
		applyList(body, "must_not_match", &p.mustNotMatch)
		a.rev++
		p.version = fmt.Sprintf("v%d", a.rev)
		reply(200, p.json())
	case len(tail) == 0 && r.Method == http.MethodDelete:
		var req struct {
			Patterns []struct {
				PatternID            int64  `json:"pattern_id"`
				CustomPatternVersion string `json:"custom_pattern_version"`
			} `json:"patterns"`
			PostDeleteAction string `json:"post_delete_action"`
		}
		_ = json.Unmarshal(mustMarshal(w, body), &req)
		if len(req.Patterns) != 1 {
			reply(422, map[string]string{"message": "expected exactly one pattern"})
			return
		}
		del := req.Patterns[0]
		p := a.patterns[del.PatternID]
		if p == nil {
			reply(404, map[string]string{})
			return
		}
		if del.CustomPatternVersion != p.version {
			reply(412, map[string]string{"message": "stale custom_pattern_version"})
			return
		}
		a.deletes++
		a.lastDeleteAction = req.PostDeleteAction
		a.lastDeleteID = del.PatternID
		delete(a.patterns, del.PatternID)
		reply(200, map[string]any{"deleted_patterns": []any{del.PatternID}})
	default:
		reply(405, map[string]string{})
	}
}

// mustMarshal re-encodes the decoded body so typed decoding sees the same JSON
// the client sent.
func mustMarshal(w http.ResponseWriter, body map[string]any) []byte {
	b, err := json.Marshal(body)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return nil
	}
	return b
}

func parseID(s string) (int64, bool) {
	var id int64
	if _, err := fmt.Sscanf(s, "%d", &id); err != nil || id <= 0 || fmt.Sprintf("%d", id) != s {
		return 0, false
	}
	return id, true
}

func sortedPatternIDs(m map[int64]*spPattern) []int64 {
	ids := make([]int64, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && ids[j] < ids[j-1]; j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}
	return ids
}

func applyDelimiter(body map[string]any, key string, dst **string) {
	v, ok := body[key]
	if !ok {
		return
	}
	if s, ok := v.(string); ok {
		*dst = &s
		return
	}
	*dst = nil
}

func applyList(body map[string]any, key string, dst *[]string) {
	v, ok := body[key]
	if !ok {
		return
	}
	if v == nil {
		*dst = nil
		return
	}
	items, _ := v.([]any)
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.(string))
	}
	*dst = out
}

func spConfig(base, name, pattern, extra string) string {
	return fmt.Sprintf(`provider "github-enterprise" {
  token          = "mock-token"
  base_url       = %q
  enterprise_slug = "acme"
}

resource "github_enterprise_secret_scanning_custom_pattern" "test" {
  provider = github-enterprise

  name    = %q
  pattern = %q
%s
}`, base, name, pattern, extra)
}

const spAddr = "github_enterprise_secret_scanning_custom_pattern.test"

// Lifecycle: create, read back without writes, update the pattern and a list,
// empty the list with a full array in the body, refuse a stale version, import
// and destroy with the provider's default post-delete action.
func TestTerraformSecretScanningCustomPattern(t *testing.T) {
	requireTerraform(t)
	api, base := newSPAPI(t)
	initial := spConfig(base, "tf-pattern", "token-[0-9]{32}", `  must_match = ["a"]`)
	retargeted := spConfig(base, "tf-pattern", "token-[0-9]{40}", `  must_match = ["b", "c"]`)
	emptied := spConfig(base, "tf-pattern", "token-[0-9]{40}", `  must_match = []`)
	stale := spConfig(base, "tf-pattern", "token-[0-9]{48}", `  must_match = []`)
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: factories, CheckDestroy: func(_ *terraform.State) error {
		api.mu.Lock()
		defer api.mu.Unlock()
		if len(api.patterns) != 0 {
			return fmt.Errorf("patterns remain after destroy: %d", len(api.patterns))
		}
		if api.creates != 1 || api.deletes != 1 {
			return fmt.Errorf("creates=%d deletes=%d, want 1 and 1", api.creates, api.deletes)
		}
		if api.lastDeleteAction != "resolve_alerts" {
			return fmt.Errorf("post_delete_action = %q, want resolve_alerts", api.lastDeleteAction)
		}
		if api.lastDeleteID != 1 {
			return fmt.Errorf("deleted pattern %d, want 1", api.lastDeleteID)
		}
		return nil
	}, Steps: []resource.TestStep{
		{Config: initial, Check: resource.ComposeTestCheckFunc(
			resource.TestCheckResourceAttr(spAddr, "id", "acme:1"),
			resource.TestCheckResourceAttr(spAddr, "enterprise_slug", "acme"),
			resource.TestCheckResourceAttr(spAddr, "pattern_id", "1"),
			resource.TestCheckResourceAttr(spAddr, "name", "tf-pattern"),
			resource.TestCheckResourceAttr(spAddr, "pattern", "token-[0-9]{32}"),
			resource.TestCheckResourceAttr(spAddr, "slug", "tf-pattern"),
			resource.TestCheckResourceAttr(spAddr, "state", "unpublished"),
			resource.TestCheckResourceAttr(spAddr, "push_protection_enabled", "false"),
			resource.TestCheckResourceAttr(spAddr, "custom_pattern_version", "v1"),
			resource.TestCheckResourceAttr(spAddr, "post_delete_action", "resolve_alerts"),
			resource.TestCheckResourceAttr(spAddr, "start_delimiter", "("),
			resource.TestCheckResourceAttr(spAddr, "must_match.#", "1"),
			resource.TestCheckResourceAttr(spAddr, "must_match.0", "a"),
		)},
		// A read-only pass must not write anything.
		{Config: initial, Check: func(_ *terraform.State) error {
			api.mu.Lock()
			defer api.mu.Unlock()
			if api.creates != 1 || api.lastPatch != nil {
				return fmt.Errorf("refresh wrote: creates=%d patch=%v", api.creates, api.lastPatch)
			}
			return nil
		}},
		{Config: retargeted, Check: resource.ComposeTestCheckFunc(
			resource.TestCheckResourceAttr(spAddr, "custom_pattern_version", "v2"),
			resource.TestCheckResourceAttr(spAddr, "pattern", "token-[0-9]{40}"),
			resource.TestCheckResourceAttr(spAddr, "must_match.#", "2"),
			func(_ *terraform.State) error {
				api.mu.Lock()
				defer api.mu.Unlock()
				body := api.lastPatch
				if body["custom_pattern_version"] != "v1" {
					return fmt.Errorf("PATCH custom_pattern_version = %v, want v1", body["custom_pattern_version"])
				}
				if body["pattern"] != "token-[0-9]{40}" {
					return fmt.Errorf("PATCH pattern = %v", body["pattern"])
				}
				list, ok := body["must_match"].([]any)
				if !ok || len(list) != 2 || list[0] != "b" || list[1] != "c" {
					return fmt.Errorf("PATCH must_match = %v, want the full new list", body["must_match"])
				}
				// Unconfigured attributes must not travel: the delimiter is
				// unchanged, and name is not updatable.
				if _, sent := body["start_delimiter"]; sent {
					return fmt.Errorf("unchanged start_delimiter sent: %v", body)
				}
				if _, sent := body["name"]; sent {
					return fmt.Errorf("name must not be sent on update: %v", body)
				}
				return nil
			},
		)},
		{Config: emptied, Check: resource.ComposeTestCheckFunc(
			resource.TestCheckResourceAttr(spAddr, "must_match.#", "0"),
			resource.TestCheckResourceAttr(spAddr, "custom_pattern_version", "v3"),
			func(_ *terraform.State) error {
				api.mu.Lock()
				defer api.mu.Unlock()
				body := api.lastPatch
				if body["custom_pattern_version"] != "v2" {
					return fmt.Errorf("PATCH custom_pattern_version = %v, want v2", body["custom_pattern_version"])
				}
				// Emptying must send the whole array, not omit the field.
				list, ok := body["must_match"].([]any)
				if !ok || list == nil || len(list) != 0 {
					return fmt.Errorf("PATCH must_match = %#v, want an empty array", body["must_match"])
				}
				return nil
			},
		)},
		// A pattern changed outside Terraform after the refresh surfaces as a
		// legible apply error, not as silent state.
		{Config: stale, PreConfig: func() {
			api.mu.Lock()
			defer api.mu.Unlock()
			api.failPatch = 412
		}, ExpectError: regexp.MustCompile("changed outside Terraform")},
		{ResourceName: spAddr, ImportState: true, ImportStateVerify: true},
	}})
}

// GitHub has no rename: changing name must destroy and recreate the pattern.
func TestTerraformSecretScanningNameReplaces(t *testing.T) {
	requireTerraform(t)
	api, base := newSPAPI(t)
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: factories, Steps: []resource.TestStep{
		{Config: spConfig(base, "tf-one", "token-[0-9]{32}", ""), Check: resource.ComposeTestCheckFunc(
			resource.TestCheckResourceAttr(spAddr, "pattern_id", "1"),
			resource.TestCheckResourceAttr(spAddr, "id", "acme:1"),
		)},
		{Config: spConfig(base, "tf-two", "token-[0-9]{32}", ""), Check: func(_ *terraform.State) error {
			api.mu.Lock()
			defer api.mu.Unlock()
			if api.creates != 2 || api.deletes != 1 {
				return fmt.Errorf("rename must replace: creates=%d deletes=%d", api.creates, api.deletes)
			}
			if _, kept := api.patterns[1]; kept {
				return fmt.Errorf("old pattern 1 still exists")
			}
			p := api.patterns[2]
			if p == nil || p.name != "tf-two" {
				return fmt.Errorf("replacement pattern missing or wrongly named: %+v", p)
			}
			return nil
		}},
	}})
}

// A pattern deleted outside Terraform drops out of state and is recreated.
func TestTerraformSecretScanningDeletedElsewhereRemovesState(t *testing.T) {
	requireTerraform(t)
	api, base := newSPAPI(t)
	cfg := spConfig(base, "tf-pattern", "token-[0-9]{32}", "")
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: factories, Steps: []resource.TestStep{
		{Config: cfg, Check: resource.TestCheckResourceAttr(spAddr, "pattern_id", "1")},
		{Config: cfg, PreConfig: func() {
			api.mu.Lock()
			defer api.mu.Unlock()
			delete(api.patterns, 1)
		}, Check: resource.ComposeTestCheckFunc(
			resource.TestCheckResourceAttr(spAddr, "pattern_id", "2"),
			func(_ *terraform.State) error {
				api.mu.Lock()
				defer api.mu.Unlock()
				if api.creates != 2 {
					return fmt.Errorf("state was not dropped and recreated: creates=%d", api.creates)
				}
				return nil
			},
		)},
	}})
}

func TestCustomPatternImportID(t *testing.T) {
	for _, bad := range []string{"", "ent", "ent:", ":1", "Ent:1", "ent:1:x", "ent/1", "ent:0", "ent:-1", "ent:01", "ent: 1", "ent:9223372036854775808"} {
		if _, _, err := parseCustomPatternImportID(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	enterprise, id, err := parseCustomPatternImportID("ent:42")
	if err != nil || enterprise != "ent" || id != 42 {
		t.Fatalf("%q %d %v", enterprise, id, err)
	}
}

// The pattern is only checked for emptiness: GitHub's Hyperscan accepts a PCRE
// subset that Go's regexp would reject, so local syntax checks would refuse
// valid patterns.
func TestCustomPatternRule(t *testing.T) {
	ctx := context.Background()
	rule := customPatternRule()
	if desc := rule.Description(ctx); !strings.Contains(desc, "Hyperscan") {
		t.Errorf("description must name the Hyperscan subset: %q", desc)
	}
	accepted := func(v string) bool {
		var resp validator.StringResponse
		rule.ValidateString(ctx, validator.StringRequest{Path: path.Root("pattern"), ConfigValue: types.StringValue(v)}, &resp)
		return !resp.Diagnostics.HasError()
	}
	for _, bad := range []string{"", "   "} {
		if accepted(bad) {
			t.Errorf("accepted %q", bad)
		}
	}
	for _, good := range []string{"token-[0-9]{32}", "(?=secret)", `prefix\K`} {
		if !accepted(good) {
			t.Errorf("rejected %q", good)
		}
	}
}
