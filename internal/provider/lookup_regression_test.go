package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/vmvarela/terraform-provider-github-enterprise/internal/client"
)

func lookupClient(t *testing.T, handler http.HandlerFunc) *client.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, err := client.New(server.URL, "mock-token", client.DefaultAPIVersion)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func teamTestState(t *testing.T, r *teamResource, imported bool) tfsdk.State {
	t.Helper()
	ctx := context.Background()
	var response resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &response)
	model := teamModel{ID: types.StringValue("acme:42"), Enterprise: types.StringValue("acme"), TeamID: types.Int64Value(42)}
	if !imported {
		model.Slug = types.StringValue("ent:old")
		model.Name = types.StringValue("Platform")
		model.Description = types.StringValue("")
		model.GroupID = types.StringValue("")
		model.Selection = types.StringValue("selected")
	}
	state := tfsdk.State{Schema: response.Schema}
	if ds := state.Set(ctx, &model); ds.HasError() {
		t.Fatal(ds)
	}
	return state
}

func TestTeamLookupRaceKeepsStateAndDoesNotWrite(t *testing.T) {
	for _, operation := range []string{"Read", "Update", "Delete"} {
		t.Run(operation, func(t *testing.T) {
			writes := 0
			c := lookupClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					writes++
					w.WriteHeader(500)
					return
				}
				if r.URL.Path == "/enterprises/acme/teams" {
					fmt.Fprint(w, `[{"id":42,"slug":"ent:intermediate"}]`)
					return
				}
				// The listed team was renamed again before its detail request.
				http.NotFound(w, r)
			})
			r := &teamResource{configuredResource{client: c}}
			ctx := context.Background()
			state := teamTestState(t, r, false)
			switch operation {
			case "Read":
				response := resource.ReadResponse{State: state}
				r.Read(ctx, resource.ReadRequest{State: state}, &response)
				if !response.Diagnostics.HasError() || !response.State.Raw.Equal(state.Raw) {
					t.Fatalf("inconclusive lookup must fail and preserve state: %v", response.Diagnostics)
				}
			case "Update":
				response := resource.UpdateResponse{State: state}
				r.Update(ctx, resource.UpdateRequest{State: state, Plan: tfsdk.Plan{Schema: state.Schema, Raw: state.Raw}}, &response)
				if !response.Diagnostics.HasError() || !response.State.Raw.Equal(state.Raw) {
					t.Fatalf("inconclusive lookup must fail and preserve state: %v", response.Diagnostics)
				}
			case "Delete":
				response := resource.DeleteResponse{State: state}
				r.Delete(ctx, resource.DeleteRequest{State: state}, &response)
				if !response.Diagnostics.HasError() || !response.State.Raw.Equal(state.Raw) {
					t.Fatalf("inconclusive lookup must not report a successful deletion: %v", response.Diagnostics)
				}
			}
			if writes != 0 {
				t.Fatalf("lookup race triggered %d writes", writes)
			}
		})
	}
}

func TestTeamReadRejectsMissingSettingsWithoutChangingState(t *testing.T) {
	for _, missing := range []string{"organization_selection_type"} {
		for _, imported := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/imported=%t", missing, imported), func(t *testing.T) {
				c := lookupClient(t, func(w http.ResponseWriter, r *http.Request) {
					team := map[string]any{"id": 42, "slug": "ent:old", "name": "Platform", "organization_selection_type": "selected"}
					delete(team, missing)
					if r.URL.Path == "/enterprises/acme/teams" {
						_ = json.NewEncoder(w).Encode([]any{team})
					} else {
						_ = json.NewEncoder(w).Encode(team)
					}
				})
				r := &teamResource{configuredResource{client: c}}
				state := teamTestState(t, r, imported)
				response := resource.ReadResponse{State: state}
				r.Read(context.Background(), resource.ReadRequest{State: state}, &response)
				if !response.Diagnostics.HasError() || !response.State.Raw.Equal(state.Raw) {
					t.Fatalf("missing settings must fail without inventing observed values: %v", response.Diagnostics)
				}
			})
		}
	}
}

func TestTeamDataSourcesPreserveMissingSettingsAsNull(t *testing.T) {
	for _, list := range []bool{false, true} {
		t.Run(fmt.Sprintf("list=%t", list), func(t *testing.T) {
			c := lookupClient(t, func(w http.ResponseWriter, r *http.Request) {
				team := `{"id":42,"slug":"ent:old","name":"Platform"}`
				if list {
					team = "[" + team + "]"
				}
				fmt.Fprint(w, team)
			})
			ctx := context.Background()
			var ds datasource.DataSource
			if list {
				ds = &teamsDataSource{configuredDataSource{client: c}}
			} else {
				ds = &teamDataSource{configuredDataSource{client: c}}
			}
			var schemaResponse datasource.SchemaResponse
			ds.Schema(ctx, datasource.SchemaRequest{}, &schemaResponse)
			input := tfsdk.State{Schema: schemaResponse.Schema}
			if list {
				if d := input.Set(ctx, &teamsModel{Enterprise: types.StringValue("acme"), Teams: types.ListNull(schemaResponse.Schema.Attributes["teams"].GetType().(types.ListType).ElemType)}); d.HasError() {
					t.Fatal(d)
				}
			} else {
				if d := input.Set(ctx, &teamModel{Enterprise: types.StringValue("acme"), Slug: types.StringValue("ent:old")}); d.HasError() {
					t.Fatal(d)
				}
			}
			response := datasource.ReadResponse{State: tfsdk.State{Schema: schemaResponse.Schema}}
			ds.Read(ctx, datasource.ReadRequest{Config: tfsdk.Config{Schema: schemaResponse.Schema, Raw: input.Raw}}, &response)
			if response.Diagnostics.HasError() {
				t.Fatal(response.Diagnostics)
			}
			if list {
				var model teamsModel
				if d := response.State.Get(ctx, &model); d.HasError() {
					t.Fatal(d)
				}
				if len(model.Teams.Elements()) != 1 {
					t.Fatal(model.Teams)
				}
				attributes := model.Teams.Elements()[0].(types.Object).Attributes()
				for _, name := range []string{"organization_selection_type"} {
					if !attributes[name].IsNull() {
						t.Fatalf("%s was guessed: %v", name, attributes[name])
					}
				}
			} else {
				var model teamModel
				if d := response.State.Get(ctx, &model); d.HasError() {
					t.Fatal(d)
				}
				if !model.Selection.IsNull() {
					t.Fatalf("settings were guessed: %v", model.Selection)
				}
			}
		})
	}
}

func TestOrganizationAssignmentsRequireObservedSelectionMode(t *testing.T) {
	c := lookupClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Error("missing mode triggered a write")
		}
		team := `{"id":42,"slug":"ent:old","name":"Platform"}`
		if r.URL.Path == "/enterprises/acme/teams" {
			team = "[" + team + "]"
		}
		fmt.Fprint(w, team)
	})
	r := &organizationsResource{configuredResource{client: c}}
	err := r.selectedTeam(context.Background(), organizationsModel{Enterprise: types.StringValue("acme"), TeamID: types.Int64Value(42)})
	if err == nil || !strings.Contains(err.Error(), "did not return organization_selection_type") {
		t.Fatalf("missing selection needs an explicit diagnostic: %v", err)
	}
}

func TestTeamReadUsesObservedSettings(t *testing.T) {
	c := lookupClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":42,"slug":"ent:old","name":"Platform","organization_selection_type":"all","notification_setting":"notifications_enabled"}`)
	})
	r := &teamResource{configuredResource{client: c}}
	state := teamTestState(t, r, false)
	response := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &response)
	if response.Diagnostics.HasError() {
		t.Fatal(response.Diagnostics)
	}
	var observed teamModel
	if ds := response.State.Get(context.Background(), &observed); ds.HasError() {
		t.Fatal(ds)
	}
	if observed.Selection.ValueString() != "all" {
		t.Fatalf("API values did not replace prior state: %v", observed.Selection)
	}
}
