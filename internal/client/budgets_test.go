package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestBudgetListFollowsHasNextPage(t *testing.T) {
	pages := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/api/enterprises/e/settings/billing/budgets" {
			t.Errorf("path %s", r.URL)
		}
		pages++
		if r.URL.Query().Get("page") == "2" {
			fmt.Fprint(w, `{"budgets":[{"id":"b2","budget_scope":"organization","budget_amount":5}],"has_next_page":false}`)
			return
		}
		fmt.Fprint(w, `{"budgets":[{"id":"b1","budget_scope":"enterprise","budget_amount":10,"prevent_further_usage":true,"budget_alerting":{"will_alert":true,"alert_recipients":["u"]}}],"has_next_page":true}`)
	})
	got, err := c.Budgets(context.Background(), "e", "", "")
	if err != nil || len(got) != 2 || pages != 2 {
		t.Fatalf("%+v %v pages=%d", got, err, pages)
	}
	if got[0].Amount != 10 || !got[0].PreventFurtherUsage || !got[0].WillAlert || got[0].AlertRecipients[0] != "u" {
		t.Errorf("first budget decoded wrong: %+v", got[0])
	}
}

func TestBudgetListRejectsBadItems(t *testing.T) {
	for _, body := range []string{`{"budgets":null}`, `{"budgets":[{"id":""}]}`, `{"budgets":[null]}`} {
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
		if _, err := c.Budgets(context.Background(), "e", "", ""); err == nil {
			t.Errorf("accepted %s", body)
		}
	}
}

func TestBudgetCreateSendsUserAndExpiry(t *testing.T) {
	var body map[string]any
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method %s", r.Method)
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("body %s: %v", raw, err)
		}
		fmt.Fprint(w, `{"budget":{"id":"b1","budget_scope":"user","budget_amount":30,"prevent_further_usage":true}}`)
	})
	in := BudgetInput{Scope: "user", User: "octocat", Amount: 30, PreventFurtherUsage: true, Type: "BundlePricing", SKU: "ai_credits", ExpiresAt: "2027-01-01"}
	b, err := c.CreateBudget(context.Background(), "e", in)
	if err != nil || b.ID != "b1" {
		t.Fatal(b, err)
	}
	if body["user"] != "octocat" || body["expires_at"] != "2027-01-01" || body["budget_amount"].(float64) != 30 {
		t.Errorf("body %v", body)
	}
	if _, ok := body["budget_entity_name"]; ok {
		t.Errorf("unused entity name sent: %v", body)
	}
	if alerting, _ := body["budget_alerting"].(map[string]any); alerting["alert_recipients"] == nil {
		t.Errorf("alert_recipients must be an array, not null: %v", body)
	}
}

func TestBudgetUpdateOnlySendsMutableFields(t *testing.T) {
	var body map[string]any
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		fmt.Fprint(w, `{"budget":{"id":"b1","budget_scope":"enterprise","budget_amount":7}}`)
	})
	if _, err := c.UpdateBudget(context.Background(), "e", "b1", BudgetInput{Amount: 7, Type: "ProductPricing", Scope: "enterprise"}); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"budget_scope", "budget_type", "budget_entity_name", "user", "expires_at"} {
		if _, ok := body[forbidden]; ok {
			t.Errorf("%s must not be sent on update: %v", forbidden, body)
		}
	}
}

func TestBudgetDeleteConfirmsID(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method %s", r.Method)
		}
		fmt.Fprint(w, `{"message":"deleted","id":"other"}`)
	})
	if err := c.DeleteBudget(context.Background(), "e", "b1"); err == nil {
		t.Error("accepted a deletion confirmation for a different budget")
	}
}

func TestBudgetReadRejectsWrongID(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"b2","budget_scope":"enterprise"}`)
	})
	if _, err := c.Budget(context.Background(), "e", "b1"); err == nil || !strings.Contains(err.Error(), "when") {
		t.Fatalf("err %v", err)
	}
}

func TestBudgetNotFoundIsTyped(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"Not Found"}`)
	})
	_, err := c.Budget(context.Background(), "e", "b1")
	if !IsNotFound(err) {
		t.Fatalf("404 must map to IsNotFound: %v", err)
	}
	if strings.Contains(err.Error(), "Not Found") {
		t.Errorf("server message leaked: %v", err)
	}
}
