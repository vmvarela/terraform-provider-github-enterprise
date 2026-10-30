package client

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type scimOps struct {
	resource string
	list     func(context.Context, *Client, string, string) ([]string, error)
	get      func(context.Context, *Client, string, string) (string, error)
}

func allSCIMOps() []scimOps {
	return []scimOps{
		{
			resource: "Groups",
			list: func(ctx context.Context, c *Client, enterprise, filter string) ([]string, error) {
				items, err := c.SCIMGroups(ctx, enterprise, filter)
				if err != nil {
					return nil, err
				}
				ids := make([]string, len(items))
				for i := range items {
					ids[i] = *items[i].ID
				}
				return ids, nil
			},
			get: func(ctx context.Context, c *Client, enterprise, id string) (string, error) {
				item, err := c.SCIMGroup(ctx, enterprise, id)
				if err != nil {
					return "", err
				}
				return *item.ID, nil
			},
		},
		{
			resource: "Users",
			list: func(ctx context.Context, c *Client, enterprise, filter string) ([]string, error) {
				items, err := c.SCIMUsers(ctx, enterprise, filter)
				if err != nil {
					return nil, err
				}
				ids := make([]string, len(items))
				for i := range items {
					ids[i] = *items[i].ID
				}
				return ids, nil
			},
			get: func(ctx context.Context, c *Client, enterprise, id string) (string, error) {
				item, err := c.SCIMUser(ctx, enterprise, id)
				if err != nil {
					return "", err
				}
				return *item.ID, nil
			},
		},
	}
}

func TestSCIMRequestPathsHeadersAndREST(t *testing.T) {
	for _, ops := range allSCIMOps() {
		t.Run(ops.resource, func(t *testing.T) {
			var calls []string
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method+" "+r.URL.EscapedPath())
				if r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("X-GitHub-Api-Version") != DefaultAPIVersion || r.Header.Get("User-Agent") == "" {
					t.Error("missing auth/version/user-agent headers")
				}
				if strings.HasPrefix(r.URL.Path, "/api/scim/") {
					if got := r.Header.Get("Accept"); got != scimAccept {
						t.Errorf("SCIM Accept %q", got)
					}
					if r.Header.Get("Content-Type") != "" {
						t.Errorf("unexpected GET Content-Type %q", r.Header.Get("Content-Type"))
					}
				} else {
					if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
						t.Errorf("REST Accept %q", got)
					}
					fmt.Fprint(w, `[]`)
					return
				}
				if r.URL.Query().Get("startIndex") != "" {
					fmt.Fprintf(w, `{"schemas":[%q],"totalResults":0,"startIndex":1,"itemsPerPage":100,"Resources":[]}`, scimListSchema)
					return
				}
				fmt.Fprint(w, `{"id":"AbC/1"}`)
			})
			ctx := context.Background()
			if id, err := ops.get(ctx, c, "a b", "AbC/1"); err != nil || id != "AbC/1" {
				t.Fatalf("get id=%q err=%v", id, err)
			}
			if ids, err := ops.list(ctx, c, "a b", ""); err != nil || len(ids) != 0 || ids == nil {
				t.Fatalf("list=%v err=%v", ids, err)
			}
			if _, err := c.Teams(ctx, "e"); err != nil {
				t.Fatal(err)
			}
			if want := "GET /api/scim/v2/enterprises/a%20b/" + ops.resource + "/" + url.PathEscape("AbC/1"); calls[0] != want {
				t.Errorf("singular path %q, want %q", calls[0], want)
			}
			if want := "GET /api/scim/v2/enterprises/a%20b/" + ops.resource; calls[1] != want {
				t.Errorf("list path %q, want %q", calls[1], want)
			}
			if calls[2] != "GET /api/enterprises/e/teams" {
				t.Errorf("REST path %q", calls[2])
			}
		})
	}
}

func TestSCIMGroupAndUserAttributes(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/scim/v2/enterprises/e/Groups/g1":
			fmt.Fprint(w, `{"id":"g1","externalId":"ext-g","displayName":"Group One"}`)
		case "/api/scim/v2/enterprises/e/Users/u1":
			fmt.Fprint(w, `{"id":"u1","externalId":"ext-u","displayName":"User One","userName":"user@example.com","active":false}`)
		case "/api/scim/v2/enterprises/e/Groups/g2":
			fmt.Fprint(w, `{"id":"g2"}`)
		case "/api/scim/v2/enterprises/e/Users/u2":
			fmt.Fprint(w, `{"id":"u2"}`)
		default:
			t.Errorf("unexpected request %s", r.URL)
		}
	})
	ctx := context.Background()
	g, err := c.SCIMGroup(ctx, "e", "g1")
	if err != nil || g.ID == nil || *g.ID != "g1" || g.ExternalID != "ext-g" || g.DisplayName != "Group One" {
		t.Fatalf("group=%+v err=%v", g, err)
	}
	u, err := c.SCIMUser(ctx, "e", "u1")
	if err != nil || u.ID == nil || *u.ID != "u1" || u.ExternalID != "ext-u" || u.UserName != "user@example.com" || u.Active {
		t.Fatalf("user=%+v err=%v", u, err)
	}
	if g, err := c.SCIMGroup(ctx, "e", "g2"); err != nil || g.ExternalID != "" || g.DisplayName != "" {
		t.Fatalf("absent group attributes not nil: %+v %v", g, err)
	}
	if u, err := c.SCIMUser(ctx, "e", "u2"); err != nil || u.ExternalID != "" || u.DisplayName != "" || u.UserName != "" || u.Active {
		t.Fatalf("absent user attributes not nil: %+v %v", u, err)
	}
}

func TestSCIMListFilterAndActualLengthPagination(t *testing.T) {
	for _, ops := range allSCIMOps() {
		t.Run(ops.resource, func(t *testing.T) {
			filter := `displayName eq "a b&page=9"`
			var indexes []string
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				if q.Get("filter") != filter {
					t.Errorf("filter %q", q.Get("filter"))
				}
				if q.Get("count") != "100" {
					t.Errorf("count %q", q.Get("count"))
				}
				indexes = append(indexes, q.Get("startIndex"))
				switch q.Get("startIndex") {
				case "1":
					fmt.Fprintf(w, `{"schemas":[%q],"totalResults":3,"startIndex":1,"itemsPerPage":100,"Resources":[{"id":"A"}]}`, scimListSchema)
				case "2":
					fmt.Fprintf(w, `{"schemas":[%q],"totalResults":3,"startIndex":2,"itemsPerPage":100,"Resources":[{"id":"B"},{"id":"C"}]}`, scimListSchema)
				default:
					t.Errorf("unexpected startIndex %q", q.Get("startIndex"))
				}
			})
			ids, err := ops.list(context.Background(), c, "e", filter)
			if err != nil || strings.Join(ids, ",") != "A,B,C" || strings.Join(indexes, ",") != "1,2" {
				t.Fatalf("ids=%v indexes=%v err=%v", ids, indexes, err)
			}
		})
	}
}

func TestSCIMValidEmptyList(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.URL.Query()["filter"]; ok {
			t.Error("empty filter was sent")
		}
		fmt.Fprintf(w, `{"schemas":[%q],"totalResults":0,"startIndex":1,"itemsPerPage":0,"Resources":[]}`, scimListSchema)
	})
	ctx := context.Background()
	groups, err := c.SCIMGroups(ctx, "e", "")
	if err != nil || groups == nil || len(groups) != 0 {
		t.Fatalf("groups=%v err=%v", groups, err)
	}
	users, err := c.SCIMUsers(ctx, "e", "")
	if err != nil || users == nil || len(users) != 0 {
		t.Fatalf("users=%v err=%v", users, err)
	}
}

func TestSCIMListRejectsInvalidResponses(t *testing.T) {
	for _, ops := range allSCIMOps() {
		for name, body := range map[string]string{
			"null-body":         `null`,
			"null-resource":     fmt.Sprintf(`{"schemas":[%q],"totalResults":1,"startIndex":1,"itemsPerPage":1,"Resources":[null]}`, scimListSchema),
			"duplicate-in-page": fmt.Sprintf(`{"schemas":[%q],"totalResults":2,"startIndex":1,"itemsPerPage":2,"Resources":[{"id":"A"},{"id":"A"}]}`, scimListSchema),
			"overflow-total":    fmt.Sprintf(`{"schemas":[%q],"totalResults":999999999999999999999,"startIndex":1,"itemsPerPage":1,"Resources":[]}`, scimListSchema),
			"missing-schemas":   `{"totalResults":0,"startIndex":1,"itemsPerPage":0,"Resources":[]}`,
			"wrong-schema":      `{"schemas":["urn:wrong"],"totalResults":0,"startIndex":1,"itemsPerPage":0,"Resources":[]}`,
			"missing-resources": fmt.Sprintf(`{"schemas":[%q],"totalResults":0,"startIndex":1,"itemsPerPage":0}`, scimListSchema),
			"null-resources":    fmt.Sprintf(`{"schemas":[%q],"totalResults":0,"startIndex":1,"itemsPerPage":0,"Resources":null}`, scimListSchema),
			"missing-total":     fmt.Sprintf(`{"schemas":[%q],"startIndex":1,"itemsPerPage":0,"Resources":[]}`, scimListSchema),
			"missing-start":     fmt.Sprintf(`{"schemas":[%q],"totalResults":0,"itemsPerPage":0,"Resources":[]}`, scimListSchema),
			"missing-per":       fmt.Sprintf(`{"schemas":[%q],"totalResults":0,"startIndex":1,"Resources":[]}`, scimListSchema),
			"negative-total":    fmt.Sprintf(`{"schemas":[%q],"totalResults":-1,"startIndex":1,"itemsPerPage":0,"Resources":[]}`, scimListSchema),
			"negative-start":    fmt.Sprintf(`{"schemas":[%q],"totalResults":0,"startIndex":-1,"itemsPerPage":0,"Resources":[]}`, scimListSchema),
			"negative-per":      fmt.Sprintf(`{"schemas":[%q],"totalResults":0,"startIndex":1,"itemsPerPage":-1,"Resources":[]}`, scimListSchema),
			"wrong-start":       fmt.Sprintf(`{"schemas":[%q],"totalResults":0,"startIndex":2,"itemsPerPage":0,"Resources":[]}`, scimListSchema),
			"overrun":           fmt.Sprintf(`{"schemas":[%q],"totalResults":1,"startIndex":1,"itemsPerPage":100,"Resources":[{"id":"A"},{"id":"B"}]}`, scimListSchema),
			"premature-empty":   fmt.Sprintf(`{"schemas":[%q],"totalResults":2,"startIndex":1,"itemsPerPage":100,"Resources":[]}`, scimListSchema),
			"missing-id":        fmt.Sprintf(`{"schemas":[%q],"totalResults":1,"startIndex":1,"itemsPerPage":1,"Resources":[{"id":""}]}`, scimListSchema),
			"malformed":         `{`,
		} {
			t.Run(ops.resource+"/"+name, func(t *testing.T) {
				c := testClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
				ids, err := ops.list(context.Background(), c, "e", "")
				if err == nil || ids != nil {
					t.Fatalf("ids=%v err=%v", ids, err)
				}
			})
		}
	}
}

func TestSCIMListRejectsDuplicateIDsAcrossPages(t *testing.T) {
	for _, ops := range allSCIMOps() {
		t.Run(ops.resource, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				index := r.URL.Query().Get("startIndex")
				fmt.Fprintf(w, `{"schemas":[%q],"totalResults":2,"startIndex":%s,"itemsPerPage":1,"Resources":[{"id":"same"}]}`, scimListSchema, index)
			})
			if ids, err := ops.list(context.Background(), c, "e", ""); err == nil || ids != nil {
				t.Fatalf("ids=%v err=%v", ids, err)
			}
		})
	}
}

func TestSCIMSingularRejectsBadIdentity(t *testing.T) {
	for _, ops := range allSCIMOps() {
		for name, tc := range map[string]struct{ selector, body string }{
			"empty-id":       {"wanted", `{"id":""}`},
			"missing-id":     {"wanted", `{}`},
			"different-id":   {"wanted", `{"id":"different"}`},
			"different-case": {"Wanted", `{"id":"wanted"}`},
			"null":           {"wanted", `null`},
			"malformed":      {"wanted", `{`},
		} {
			t.Run(ops.resource+"/"+name, func(t *testing.T) {
				c := testClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, tc.body) })
				id, err := ops.get(context.Background(), c, "e", tc.selector)
				if err == nil || id != "" {
					t.Fatalf("id=%q err=%v", id, err)
				}
			})
		}
	}
}

func TestSCIMDotIDsRejectedBeforeRequest(t *testing.T) {
	calls := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, `{"id":"x"}`)
	})
	for _, ops := range allSCIMOps() {
		for _, id := range []string{"", ".", ".."} {
			if got, err := ops.get(context.Background(), c, "e", id); err == nil || got != "" {
				t.Errorf("%s accepted ID %q", ops.resource, id)
			}
		}
	}
	if calls != 0 {
		t.Fatalf("requests sent: %d", calls)
	}
}

func TestSCIMErrorsAreNotRetriedOrLeaked(t *testing.T) {
	for _, ops := range allSCIMOps() {
		for _, status := range []int{400, 401, 403, 404, 429, 500} {
			t.Run(fmt.Sprintf("%s/%d", ops.resource, status), func(t *testing.T) {
				calls := 0
				c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
					calls++
					w.Header().Set("Retry-After", "30")
					w.WriteHeader(status)
					fmt.Fprint(w, `{"message":"test-token"}`)
				})
				ctx := context.Background()
				_, listErr := ops.list(ctx, c, "e", "")
				_, getErr := ops.get(ctx, c, "e", "id")
				if calls != 2 {
					t.Fatalf("calls %d", calls)
				}
				for _, err := range []error{listErr, getErr} {
					if err == nil || strings.Contains(err.Error(), "test-token") || (status == 404) != IsNotFound(err) {
						t.Fatalf("status %d err %v", status, err)
					}
				}
			})
		}
	}
}

func TestSCIMRedirectNotFollowed(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("redirect was followed")
	}))
	defer target.Close()
	for _, ops := range allSCIMOps() {
		t.Run(ops.resource, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
			})
			if _, err := ops.list(context.Background(), c, "e", ""); err == nil {
				t.Fatal("accepted redirect for list")
			}
			if _, err := ops.get(context.Background(), c, "e", "id"); err == nil {
				t.Fatal("accepted redirect for get")
			}
		})
	}
}
