package provider

import (
	"context"
	"github.com/google/go-github/v93/github"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/vmvarela/terraform-provider-github-enterprise/internal/client"
)

func TestPartialOrganizationUpdateRetainsObservedState(t *testing.T) {
	ctx := context.Background()
	api, base := newAPI(t)
	api.store("acme").team = &github.EnterpriseTeam{ID: 42, Name: "Platform", Slug: "ent:platform", OrganizationSelectionType: new("selected")}
	api.store("acme").orgs["org-one"] = true
	api.failRemoval = true
	c, err := client.New(base, "mock-token", client.DefaultAPIVersion)
	if err != nil {
		t.Fatal(err)
	}
	r := &organizationsResource{configuredResource{client: c}}
	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	oldSet, ds := types.SetValueFrom(ctx, types.StringType, []string{"org-one"})
	if ds.HasError() {
		t.Fatal(ds)
	}
	newSet, ds := types.SetValueFrom(ctx, types.StringType, []string{"org-two"})
	if ds.HasError() {
		t.Fatal(ds)
	}
	old := organizationsModel{ID: types.StringValue("acme/42"), Enterprise: types.StringValue("acme"), TeamID: types.Int64Value(42), Organizations: oldSet}
	planned := old
	planned.Organizations = newSet
	state := tfsdk.State{Schema: schemaResp.Schema}
	if ds := state.Set(ctx, &old); ds.HasError() {
		t.Fatal(ds)
	}
	planState := tfsdk.State{Schema: schemaResp.Schema}
	if ds := planState.Set(ctx, &planned); ds.HasError() {
		t.Fatal(ds)
	}
	plan := tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw}
	first := resource.UpdateResponse{State: state}
	r.Update(ctx, resource.UpdateRequest{Plan: plan, State: state}, &first)
	if !first.Diagnostics.HasError() {
		t.Fatal("expected partial failure")
	}
	var observed organizationsModel
	if ds := first.State.Get(ctx, &observed); ds.HasError() {
		t.Fatal(ds)
	}
	if len(observed.Organizations.Elements()) != 2 {
		t.Fatalf("partial state not retained: %v", observed.Organizations)
	}
	second := resource.UpdateResponse{State: first.State}
	r.Update(ctx, resource.UpdateRequest{Plan: plan, State: first.State}, &second)
	if second.Diagnostics.HasError() {
		t.Fatal(second.Diagnostics)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.store("acme").orgs) != 1 || !api.store("acme").orgs["org-two"] {
		t.Fatalf("retry failed to reconcile: %v", api.store("acme").orgs)
	}
}

func TestForbiddenTeamReadKeepsState(t *testing.T) {
	ctx := context.Background()
	api, base := newAPI(t)
	api.deny = true
	c, err := client.New(base, "mock-token", client.DefaultAPIVersion)
	if err != nil {
		t.Fatal(err)
	}
	r := &teamResource{configuredResource{client: c}}
	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	data := teamModel{ID: types.StringValue("acme/42"), Enterprise: types.StringValue("acme"), TeamID: types.Int64Value(42), Slug: types.StringValue("ent:platform"), Name: types.StringValue("Platform"), Description: types.StringValue(""), GroupID: types.StringValue(""), Selection: types.StringValue("disabled")}
	state := tfsdk.State{Schema: schemaResp.Schema}
	if ds := state.Set(ctx, &data); ds.HasError() {
		t.Fatal(ds)
	}
	response := resource.ReadResponse{State: state}
	r.Read(ctx, resource.ReadRequest{State: state}, &response)
	if !response.Diagnostics.HasError() || !response.State.Raw.Equal(state.Raw) {
		t.Fatal("permission error dropped or modified resource state")
	}
}

func TestMissingTeamReadRemovesState(t *testing.T) {
	ctx := context.Background()
	_, base := newAPI(t)
	c, err := client.New(base, "mock-token", client.DefaultAPIVersion)
	if err != nil {
		t.Fatal(err)
	}
	r := &teamResource{configuredResource{client: c}}
	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	data := teamModel{ID: types.StringValue("acme/42"), Enterprise: types.StringValue("acme"), TeamID: types.Int64Value(42), Slug: types.StringValue("ent:platform"), Name: types.StringValue("Platform"), Description: types.StringValue(""), GroupID: types.StringValue(""), Selection: types.StringValue("disabled")}
	state := tfsdk.State{Schema: schemaResp.Schema}
	if ds := state.Set(ctx, &data); ds.HasError() {
		t.Fatal(ds)
	}
	response := resource.ReadResponse{State: state}
	r.Read(ctx, resource.ReadRequest{State: state}, &response)
	if response.Diagnostics.HasError() || !response.State.Raw.IsNull() {
		t.Fatalf("missing team was not removed: %v", response.Diagnostics)
	}
}
