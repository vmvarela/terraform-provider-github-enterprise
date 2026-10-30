package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/go-github/v93/github"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	c, err := New(s.URL+"/api/", "test-token", DefaultAPIVersion)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func TestClientSendsConfiguredAPIVersion(t *testing.T) {
	var got string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-GitHub-Api-Version")
		fmt.Fprint(w, `[]`)
	}))
	defer s.Close()
	// Outside the range the SDK accepts, to prove the transport header survives.
	c, err := New(s.URL+"/api/", "test-token", "2027-01-01")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Teams(context.Background(), "acme"); err != nil {
		t.Fatal(err)
	}
	if got != "2027-01-01" {
		t.Errorf("api version %q", got)
	}
}

func TestFindTeamDoesNotFollowReusedSlug(t *testing.T) {
	var paths []string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/api/enterprises/acme/teams/ent:old":
			fmt.Fprint(w, `{"id":99,"slug":"ent:old"}`)
		case "/api/enterprises/acme/teams":
			fmt.Fprint(w, `[{"id":42,"slug":"ent:new"}]`)
		case "/api/enterprises/acme/teams/ent:new":
			fmt.Fprint(w, `{"id":42,"name":"New","slug":"ent:new"}`)
		default:
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
		}
	})
	team, err := c.FindTeam(context.Background(), "acme", 42, "ent:old")
	if err != nil || team.ID != 42 || team.Slug != "ent:new" {
		t.Fatalf("team=%+v err=%v", team, err)
	}
	if len(paths) != 3 {
		t.Fatalf("requests: %v", paths)
	}
}
func TestFindTeamFastPathAndForbidden(t *testing.T) {
	for _, status := range []int{200, 403, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(status)
				fmt.Fprint(w, `{"id":42,"slug":"ent:platform"}`)
			})
			_, err := c.FindTeam(context.Background(), "acme", 42, "ent:platform")
			if (status == 200) != (err == nil) || calls != 1 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}
func TestPaginationUsesLocalEndpointAndAllPages(t *testing.T) {
	calls := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("X-GitHub-Api-Version") != DefaultAPIVersion {
			t.Error("missing API headers")
		}
		if r.URL.Query().Get("per_page") != "100" {
			t.Error("unexpected page size")
		}
		switch r.URL.Query().Get("page") {
		case "1":
			w.Header().Set("Link", `<https://untrusted.example/elsewhere?page=2>; rel="next"`)
			fmt.Fprint(w, `[{"id":1,"slug":"ent:one"}]`)
		case "2":
			fmt.Fprint(w, `[{"id":2,"slug":"ent:two"}]`)
		default:
			t.Error("unexpected page")
		}
	})
	teams, err := c.Teams(context.Background(), "acme")
	if err != nil || len(teams) != 2 || calls != 2 {
		t.Fatalf("teams=%v calls=%d err=%v", teams, calls, err)
	}
}
func TestPaginationRejectsLoop(t *testing.T) {
	calls := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Link", `<https://api.github.com/x?page=1>; rel="next"`)
		fmt.Fprint(w, `[]`)
	})
	_, err := c.Teams(context.Background(), "acme")
	if err == nil || !strings.Contains(err.Error(), "invalid next page") {
		t.Fatalf("accepted a pagination loop: %v", err)
	}
	if calls != 1 {
		t.Errorf("requested %d pages before rejecting the loop", calls)
	}
}
func TestUpdateCanExplicitlyClearGroupAndDescription(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PATCH" {
			t.Error(r.Method)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if v, ok := body["group_id"]; !ok || v != nil {
			t.Errorf("missing explicit null: %v", body)
		}
		if v, ok := body["description"]; !ok || v != "" {
			t.Errorf("missing empty description: %v", body)
		}
		fmt.Fprint(w, `{"id":42,"slug":"ent:platform"}`)
	})
	if _, err := c.UpdateTeam(context.Background(), "acme", "ent:platform", map[string]any{"group_id": nil, "description": ""}); err != nil {
		t.Fatal(err)
	}
}
func TestMembershipUsesNumericEndpointWithoutTeamListings(t *testing.T) {
	var methods []string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/enterprises/acme/teams/42/memberships/octocat" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		methods = append(methods, r.Method)
		if r.Method == "DELETE" {
			w.WriteHeader(204)
			return
		}
		fmt.Fprint(w, `{"login":"octocat","id":7}`)
	})
	ctx := context.Background()
	if _, err := c.AddMember(ctx, "acme", 42, "octocat"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Membership(ctx, "acme", 42, "octocat"); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveMember(ctx, "acme", 42, "octocat"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(methods, ",") != "PUT,GET,DELETE" {
		t.Fatal(methods)
	}
}
func TestErrorsDoNotLeakResponseBodies(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.Header().Set("X-GitHub-Request-Id", "req-1")
		w.WriteHeader(429)
		fmt.Fprint(w, `{"message":"test-token"}`)
	})
	_, err := c.Teams(context.Background(), "acme")
	if err == nil {
		t.Fatal("missing error")
	}
	if strings.Contains(err.Error(), "test-token") || !strings.Contains(err.Error(), "req-1") || !strings.Contains(err.Error(), "30") {
		t.Fatal(err)
	}
}
func TestRedirectDoesNotForwardToken(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("redirect was followed") }))
	defer target.Close()
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	})
	if _, err := c.Teams(context.Background(), "acme"); err == nil {
		t.Fatal("accepted redirect")
	}
}
func TestClientValidation(t *testing.T) {
	for _, base := range []string{"ftp://api.github.com/", "https://u:p@api.github.com/", "https://api.github.com/?x=y", "http://example.com/", ""} {
		if _, err := New(base, "token", DefaultAPIVersion); err == nil {
			t.Errorf("accepted %q", base)
		}
	}
	if _, err := New("https://api.github.com/", "", "2026-03-10"); err == nil {
		t.Fatal("accepted empty token")
	}
	for _, token := range []string{"token\n", "token\t", "token\x7f", "secret value"} {
		if _, err := New("https://api.github.com/", token, DefaultAPIVersion); err == nil {
			t.Fatal("accepted invalid header characters in token")
		}
	}
	if _, err := New("https://api.github.com/", "token", "invalid"); err == nil {
		t.Fatal("accepted invalid API version")
	}
}

func TestFindTeamListedSlugFailureIsInconclusive(t *testing.T) {
	for _, status := range []int{404, 403, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/enterprises/acme/teams":
					fmt.Fprint(w, `[{"id":42,"slug":"ent:intermediate"}]`)
				default:
					w.WriteHeader(status)
				}
			})
			_, err := c.FindTeam(context.Background(), "acme", 42, "")
			if err == nil || IsNotFound(err) {
				t.Fatalf("positive ID lookup must not become conclusive absence: %v", err)
			}
			if status == 404 && !strings.Contains(err.Error(), "retry") {
				t.Fatalf("expected actionable retry diagnostic: %v", err)
			}
			if status != 404 && !strings.Contains(err.Error(), fmt.Sprintf("HTTP %d", status)) {
				t.Fatalf("lost original API failure: %v", err)
			}
		})
	}
}

func TestTeamSettingPresence(t *testing.T) {
	for _, tc := range []struct {
		name, fields string
		present      bool
	}{
		{"omitted", "", false},
		{"null", `,"organization_selection_type":null`, false},
		{"explicit", `,"organization_selection_type":"selected"`, true},
		{"empty", `,"organization_selection_type":""`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var team github.EnterpriseTeam
			if err := json.Unmarshal([]byte(`{"id":42,"slug":"ent:platform"`+tc.fields+`}`), &team); err != nil {
				t.Fatal(err)
			}
			if (team.OrganizationSelectionType != nil) != tc.present {
				t.Fatalf("lost field presence: %+v", team)
			}
			if tc.name == "explicit" && *team.OrganizationSelectionType != "selected" {
				t.Fatalf("lost observed settings: %+v", team)
			}
			if tc.name == "empty" && *team.OrganizationSelectionType != "" {
				t.Fatalf("empty values were rewritten: %+v", team)
			}
		})
	}
}

func TestPathsEscapeOpaqueIDs(t *testing.T) {
	var got string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.EscapedPath()
		fmt.Fprint(w, `{"id":7,"slug":"a/b c"}`)
	})
	if _, err := c.TeamBySlug(context.Background(), "acme", "a/b c"); err != nil {
		t.Fatal(err)
	}
	if want := "/api/enterprises/acme/teams/a%2Fb%20c"; got != want {
		t.Errorf("path %q, want %q", got, want)
	}
}

func TestListRejectsEmptyResponseBody(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if _, err := c.Teams(context.Background(), "acme"); err == nil {
		t.Fatal("accepted an empty list response as an empty team list")
	}
}

func TestListRejectsNullElement(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[null]`)
	})
	if _, err := c.Teams(context.Background(), "acme"); err == nil {
		t.Fatal("accepted a null team")
	}
}

func TestMembershipWithoutUserIsRejected(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{}`)
	})
	if _, err := c.Membership(context.Background(), "acme", 7, "someone"); err == nil {
		t.Fatal("accepted a membership without a user")
	}
}

func TestAcceptedStatusIsAnError(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"message":"accepted"}`)
	})
	_, err := c.Teams(context.Background(), "acme")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusAccepted {
		t.Fatalf("err = %v, want an APIError with status 202", err)
	}
}

func TestTwoFactorAuthErrorDoesNotLeakTheResponse(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-GitHub-OTP", "required; sms")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"message":"secret-echo"}`)
	})
	_, err := c.Teams(context.Background(), "acme")
	if err == nil || strings.Contains(err.Error(), "secret-echo") {
		t.Fatalf("err = %v, want a sanitized error", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized {
		t.Fatalf("err = %v, want an APIError with status 401", err)
	}
}

func TestDecodeErrorDoesNotEchoServerValue(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":7,"slug":"x","created_at":"secret-echo"}`)
	})
	_, err := c.TeamBySlug(context.Background(), "acme", "x")
	if err == nil || strings.Contains(err.Error(), "secret-echo") {
		t.Fatalf("err = %v, want a sanitized error", err)
	}
}
