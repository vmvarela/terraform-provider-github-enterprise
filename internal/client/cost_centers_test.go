package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCostCenterListWrapperAndNextLink(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/enterprises/a%20b/settings/billing/cost-centers" && r.URL.EscapedPath() != "/api/enterprises/a%20b/settings/billing/cost-centers" {
			t.Errorf("path %s", r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("X-GitHub-Api-Version") != DefaultAPIVersion {
			t.Error("headers")
		}
		if r.URL.Query().Get("state") != "active" {
			t.Errorf("state filter lost: %s", r.URL)
		}
		if r.URL.Query().Get("page") == "2" {
			fmt.Fprint(w, `{"costCenters":[{"id":"2","name":"B","resources":[]}]}`)
			return
		}
		w.Header().Set("Link", `<https://evil.example/x?page=2>; rel="next"`)
		fmt.Fprint(w, `{"costCenters":[{"id":"1","name":"A","state":"active","resources":[{"type":"User","name":"u"}]}]}`)
	})
	got, err := c.CostCenters(context.Background(), "a b", "active")
	if err != nil || len(got) != 2 || got[0].Resources[0].Type != "User" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestCostCenterListRejectsBadItems(t *testing.T) {
	for _, body := range []string{`{"costCenters":[{"id":"","name":"A"}]}`, `{"costCenters":[{"id":"1","name":"A","resources":[]},{"id":"1","name":"B","resources":[]}]}`} {
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
		if _, err := c.CostCenters(context.Background(), "e", ""); err == nil {
			t.Errorf("accepted %s", body)
		}
	}
}

func TestCostCenterCreateUpdatePayloads(t *testing.T) {
	var calls []string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls = append(calls, r.Method+" "+r.URL.EscapedPath()+" "+strings.TrimSuffix(string(b), "\n"))
		if r.Header.Get("Content-Type") != "application/json" {
			t.Error("content type")
		}
		fmt.Fprint(w, `{"id":"x/1","name":"N"}`)
	})
	if cc, err := c.CreateCostCenter(context.Background(), "e", "N"); err != nil || cc.ID != "x/1" {
		t.Fatal(cc, err)
	}
	if _, err := c.UpdateCostCenter(context.Background(), "e", "x/1", "N"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.UpdateCostCenter(context.Background(), "e", "other", "N"); err == nil {
		t.Error("identity change accepted")
	}
	want := `POST /api/enterprises/e/settings/billing/cost-centers {"name":"N"}`
	if calls[0] != want || calls[1] != `PATCH /api/enterprises/e/settings/billing/cost-centers/x%2F1 {"name":"N"}` {
		t.Fatalf("%q", calls)
	}
}

func TestCostCenterReadRequiresResourcesButNotState(t *testing.T) {
	body := `{"id":"1","name":"A","resources":[]}`
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
	cc, err := c.CostCenter(context.Background(), "e", "1")
	if err != nil || cc.State != nil || cc.Resources == nil {
		t.Fatal(cc, err)
	}
	body = `{"id":"1","name":"A"}`
	if _, err := c.CostCenter(context.Background(), "e", "1"); err == nil {
		t.Fatal("missing resources accepted")
	}
	if _, err := c.CostCenters(context.Background(), "e", ""); err == nil {
		t.Fatal("list accepted non-wrapper")
	}
}

func TestCostCenterCreateWithoutIdentityFails(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"name":"N"}`) })
	if _, err := c.CreateCostCenter(context.Background(), "e", "N"); err == nil {
		t.Fatal("accepted")
	}
}

func TestCostCenterGetMultiplePages(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("per_page") != "100" || r.Method != "GET" {
			t.Error("query")
		}
		switch q.Get("page") {
		case "1":
			fmt.Fprint(w, `{"id":"1","name":"A","state":"active","has_next_page":true,"resources":[{"type":"User","name":"u"}]}`)
		case "2":
			fmt.Fprint(w, `{"id":"1","name":"A","state":"active","has_next_page":false,"resources":[{"type":"Repo","name":"r"}]}`)
		default:
			t.Error("page")
		}
	})
	cc, err := c.CostCenter(context.Background(), "e", "1")
	if err != nil || len(cc.Resources) != 2 || cc.Resources[1].Name != "r" {
		t.Fatalf("%+v %v", cc, err)
	}
}

func TestCostCenterGetRejectsInconsistent(t *testing.T) {
	for name, pages := range map[string][2]string{
		"id":      {`{"id":"1","name":"A","has_next_page":true,"resources":[{"type":"U","name":"u"}]}`, `{"id":"2","name":"A","has_next_page":false,"resources":[]}`},
		"name":    {`{"id":"1","name":"A","has_next_page":true,"resources":[{"type":"U","name":"u"}]}`, `{"id":"1","name":"B","has_next_page":false,"resources":[]}`},
		"flag":    {`{"id":"1","name":"A","has_next_page":true,"resources":[{"type":"U","name":"u"}]}`, `{"id":"1","name":"A","resources":[]}`},
		"empty":   {`{"id":"1","name":"A","has_next_page":true}`, ``},
		"noname":  {`{"id":"1"}`, ``},
		"nothing": {`{"name":"A"}`, ``},
	} {
		t.Run(name, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("page") == "1" {
					fmt.Fprint(w, pages[0])
				} else {
					fmt.Fprint(w, pages[1])
				}
			})
			if _, err := c.CostCenter(context.Background(), "e", "1"); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"id":"9","name":"A"}`) })
	if _, err := c.CostCenter(context.Background(), "e", "1"); err == nil {
		t.Fatal("identity change accepted")
	}
}

func TestCostCenterGetNeverEndingPagesBounded(t *testing.T) {
	calls := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, `{"id":"1","name":"A","has_next_page":true,"resources":[{"type":"U","name":"u"}]}`)
	})
	if _, err := c.CostCenter(context.Background(), "e", "1"); err == nil || calls != maxPages {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestCostCenterArchive(t *testing.T) {
	body := `{"message":"ok","id":"1","name":"A","costCenterState":"CostCenterArchived"}`
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "DELETE" || r.URL.Path != "/api/enterprises/e/settings/billing/cost-centers/1" {
			t.Error(r.Method, r.URL)
		}
		fmt.Fprint(w, body)
	})
	if err := c.DeleteCostCenter(context.Background(), "e", "1"); err != nil {
		t.Fatal(err)
	}
	body = `{"id":"1","costCenterState":"active"}`
	if err := c.DeleteCostCenter(context.Background(), "e", "1"); err == nil {
		t.Fatal("unconfirmed archive accepted")
	}
}

func TestCostCenterResources(t *testing.T) {
	var got []string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.Path+" "+strings.TrimSuffix(string(b), "\n"))
		fmt.Fprint(w, `{"message":"m","reassigned_resources":[{"resource_type":"User","name":"u","previous_cost_center":"old"},{"resource_type":"Repo","name":"r","previous_cost_center":"older"}]}`)
	})
	res, err := c.AddCostCenterResources(context.Background(), "e", "1", CostCenterAssignments{Users: []string{"u"}, EnterpriseTeams: []string{"t"}})
	if err != nil || len(res) != 2 || res[1].PreviousCostCenter != "older" {
		t.Fatal(res, err)
	}
	if err := c.RemoveCostCenterResources(context.Background(), "e", "1", CostCenterAssignments{Repositories: []string{"o/r"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveCostCenterResources(context.Background(), "e", "1", CostCenterAssignments{}); err != nil {
		t.Fatal(err)
	}
	p := "/api/enterprises/e/settings/billing/cost-centers/1/resource "
	if len(got) != 2 || got[0] != "POST "+p+`{"users":["u"],"enterprise_teams":["t"]}` || got[1] != "DELETE "+p+`{"repositories":["o/r"]}` {
		t.Fatalf("%q", got)
	}
}

func TestCostCenterErrorsAreNotRetried(t *testing.T) {
	for _, status := range []int{403, 404, 429, 500, 502} {
		calls := 0
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.WriteHeader(status)
			fmt.Fprint(w, `{"message":"secret-echo"}`)
		})
		ctx := context.Background()
		_, e1 := c.CreateCostCenter(ctx, "e", "N")
		_, e2 := c.AddCostCenterResources(ctx, "e", "1", CostCenterAssignments{Users: []string{"u"}})
		e3 := c.DeleteCostCenter(ctx, "e", "1")
		_, e4 := c.CostCenter(ctx, "e", "1")
		if calls != 4 {
			t.Fatalf("status %d calls %d", status, calls)
		}
		for _, err := range []error{e1, e2, e3, e4} {
			if err == nil || strings.Contains(err.Error(), "secret-echo") || (status == 404) != IsNotFound(err) {
				t.Fatalf("status %d err %v", status, err)
			}
		}
	}
}

func TestCostCenterGetMissingFlagOnFullPageFails(t *testing.T) {
	res := strings.Repeat(`{"type":"User","name":"u"},`, costCenterPageSize)
	body := `{"id":"1","name":"A","resources":[` + strings.TrimSuffix(res, ",") + `]}`
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
	if _, err := c.CostCenter(context.Background(), "e", "1"); err == nil {
		t.Fatal("full page without has_next_page accepted")
	}
	body = `{"id":"1","name":"A","resources":[{"type":"User","name":"u"}]}`
	if cc, err := c.CostCenter(context.Background(), "e", "1"); err != nil || len(cc.Resources) != 1 {
		t.Fatal(cc, err)
	}
}

func TestCostCenterDotIDsRejected(t *testing.T) {
	calls := 0
	body := `{"id":"..","name":"N"}`
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls++; fmt.Fprint(w, body) })
	ctx := context.Background()
	for _, id := range []string{"", ".", ".."} {
		if _, err := c.CostCenter(ctx, "e", id); err == nil {
			t.Error("read", id)
		}
		if _, err := c.UpdateCostCenter(ctx, "e", id, "N"); err == nil {
			t.Error("update", id)
		}
		if err := c.DeleteCostCenter(ctx, "e", id); err == nil {
			t.Error("delete", id)
		}
		if err := c.RemoveCostCenterResources(ctx, "e", id, CostCenterAssignments{Users: []string{"u"}}); err == nil {
			t.Error("remove", id)
		}
	}
	if calls != 0 {
		t.Fatalf("requests sent: %d", calls)
	}
	if cc, err := c.CreateCostCenter(ctx, "e", "N"); err == nil || cc != nil {
		t.Fatal("dot ID accepted on create", cc)
	}
}

func TestCostCenterCreateIncompleteKeepsIdentity(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"id":"abc"}`) })
	cc, err := c.CreateCostCenter(context.Background(), "e", "N")
	if err == nil || cc == nil || cc.ID != "abc" || !strings.Contains(err.Error(), "refresh") {
		t.Fatal(cc, err)
	}
}
