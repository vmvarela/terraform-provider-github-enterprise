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

func TestCustomPatternCreateRequiresExactlyOne(t *testing.T) {
	for name, body := range map[string]string{
		"empty":   `{"created_patterns":[]}`,
		"null":    `{"created_patterns":null}`,
		"two":     `{"created_patterns":[{"id":1},{"id":2}]}`,
		"no id":   `{"created_patterns":[{"id":0}]}`,
		"missing": `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, body) })
			if _, err := c.CreateCustomPattern(context.Background(), "e", CustomPatternInput{Name: "n", Pattern: "p"}); err == nil {
				t.Fatal("expected an error for a response that does not confirm one created pattern")
			}
		})
	}
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"created_patterns":[{"id":7,"name":"n","pattern":"p","slug":"n","state":"unpublished","custom_pattern_version":"v1"}]}`)
	})
	p, err := c.CreateCustomPattern(context.Background(), "e", CustomPatternInput{Name: "n", Pattern: "p"})
	if err != nil || p.ID != 7 || p.Version != "v1" || p.State != "unpublished" {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestCustomPatternUpdateSendsExplicitFieldsAndVersion(t *testing.T) {
	var raw []byte
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method %s", r.Method)
		}
		raw, _ = io.ReadAll(r.Body)
		fmt.Fprint(w, `{"id":7,"name":"n","pattern":"p","slug":"n","state":"unpublished","custom_pattern_version":"v2"}`)
	})
	patch := map[string]json.RawMessage{
		"must_match":      json.RawMessage("[]"),
		"start_delimiter": json.RawMessage("null"),
	}
	p, err := c.UpdateCustomPattern(context.Background(), "e", 7, "v1", patch)
	if err != nil || p.Version != "v2" {
		t.Fatalf("%+v %v", p, err)
	}
	// The omitempty-free raw body is the only way to express an emptied list or
	// a cleared delimiter, and every update carries the version it was read at.
	for _, want := range []string{`"must_match":[]`, `"start_delimiter":null`, `"custom_pattern_version":"v1"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("body %s lacks %s", raw, want)
		}
	}
}

func TestCustomPatternUpdateWrapsPreconditionFailure(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPreconditionFailed)
		fmt.Fprint(w, `{"message":"stale custom_pattern_version"}`)
	})
	_, err := c.UpdateCustomPattern(context.Background(), "e", 7, "v1", map[string]json.RawMessage{"pattern": json.RawMessage(`"p"`)})
	if err == nil || !strings.Contains(err.Error(), "changed outside Terraform") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "stale custom_pattern_version") {
		t.Errorf("server message leaked: %v", err)
	}
}

func TestCustomPatternDeleteTreatsMissingAsGone(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method %s", r.Method)
		}
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"Not Found"}`)
	})
	if err := c.DeleteCustomPattern(context.Background(), "e", 7, "v1", "resolve_alerts"); err != nil {
		t.Fatalf("a pattern that is already gone must not fail the delete: %v", err)
	}
}

func TestCustomPatternListPaginates(t *testing.T) {
	pages := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		pages++
		if r.URL.Query().Get("per_page") != "100" {
			t.Errorf("per_page = %q, want 100", r.URL.Query().Get("per_page"))
		}
		if r.URL.Query().Get("page") == "2" {
			fmt.Fprint(w, `[{"id":2,"name":"b","pattern":"p","slug":"b","state":"unpublished","custom_pattern_version":"v1"}]`)
			return
		}
		w.Header().Set("Link", `<https://api.github.com/enterprises/e/secret-scanning/custom-patterns?page=2>; rel="next"`)
		fmt.Fprint(w, `[{"id":1,"name":"a","pattern":"p","slug":"a","state":"unpublished","custom_pattern_version":"v1"}]`)
	})
	got, err := c.ListCustomPatterns(context.Background(), "e")
	if err != nil || len(got) != 2 || pages != 2 {
		t.Fatalf("%+v %v pages=%d", got, err, pages)
	}
	if got[0].ID != 1 || got[1].ID != 2 {
		t.Errorf("order wrong: %+v", got)
	}
	// The provider reads unpublished patterns too, so there is no state filter.
	p, err := c.GetCustomPattern(context.Background(), "e", 2)
	if err != nil || p == nil || p.Name != "b" {
		t.Fatalf("%+v %v", p, err)
	}
	if p, err := c.GetCustomPattern(context.Background(), "e", 9); err != nil || p != nil {
		t.Fatalf("a missing pattern must be (nil, nil): %+v %v", p, err)
	}
}
