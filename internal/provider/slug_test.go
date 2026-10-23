package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	pschema "github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// The slug belongs to the provider; every block inherits it unless it sets its own.
func TestEnterpriseSlugIsInheritedFromProvider(t *testing.T) {
	ctx := context.Background()
	p := &enterpriseProvider{}
	var pResp provider.SchemaResponse
	p.Schema(ctx, provider.SchemaRequest{}, &pResp)
	if attr, ok := pResp.Schema.Attributes["enterprise_slug"].(pschema.StringAttribute); !ok || !attr.Optional {
		t.Fatalf("provider enterprise_slug must be an optional attribute, got %#v", pResp.Schema.Attributes["enterprise_slug"])
	}
	for _, factory := range p.Resources(ctx) {
		r := factory()
		var resp resource.SchemaResponse
		r.Schema(ctx, resource.SchemaRequest{}, &resp)
		if attr, ok := resp.Schema.Attributes["enterprise_slug"].(rschema.StringAttribute); !ok || !attr.Optional || !attr.Computed {
			t.Errorf("%T: enterprise_slug must be optional and computed", r)
		}
	}
	for _, factory := range p.DataSources(ctx) {
		d := factory()
		var resp datasource.SchemaResponse
		d.Schema(ctx, datasource.SchemaRequest{}, &resp)
		if attr, ok := resp.Schema.Attributes["enterprise_slug"].(dsschema.StringAttribute); !ok || !attr.Optional || !attr.Computed {
			t.Errorf("%T: enterprise_slug must be optional and computed", d)
		}
	}
}

func TestModifyPlanFillsProviderSlug(t *testing.T) {
	ctx := context.Background()
	r := &teamResource{configuredResource{enterpriseSlug: "provider-enterprise"}}
	var resp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &resp)

	// Everything null: the block sets no slug, as a configuration that relies on
	// the provider value would.
	schemaType := resp.Schema.Type().TerraformType(ctx).(tftypes.Object)
	values := map[string]tftypes.Value{}
	for name, attrType := range schemaType.AttributeTypes {
		values[name] = tftypes.NewValue(attrType, nil)
	}
	raw := tftypes.NewValue(schemaType, values)
	plan := tfsdk.Plan{Schema: resp.Schema, Raw: raw}

	var mp resource.ModifyPlanResponse
	mp.Plan = plan
	r.ModifyPlan(ctx, resource.ModifyPlanRequest{Config: tfsdk.Config{Schema: resp.Schema, Raw: raw}, Plan: plan}, &mp)
	if mp.Diagnostics.HasError() {
		t.Fatalf("ModifyPlan reported %v", mp.Diagnostics)
	}
	var got types.String
	mp.Plan.GetAttribute(ctx, path.Root("enterprise_slug"), &got)
	if got.ValueString() != "provider-enterprise" {
		t.Fatalf("planned enterprise_slug = %q, want the provider value", got.ValueString())
	}

	// With neither a provider value nor an environment default the plan fails
	// instead of sending an empty slug to the API.
	r = &teamResource{}
	mp = resource.ModifyPlanResponse{Plan: plan}
	r.ModifyPlan(ctx, resource.ModifyPlanRequest{Config: tfsdk.Config{Schema: resp.Schema, Raw: raw}, Plan: plan}, &mp)
	if !mp.Diagnostics.HasError() {
		t.Fatal("ModifyPlan accepted a missing enterprise slug")
	}
}

func TestDataSourceSlugFallback(t *testing.T) {
	d := &configuredDataSource{enterpriseSlug: "provider-enterprise"}
	if slug, ok := d.enterpriseSlugOr(types.StringValue("explicit")); !ok || slug.ValueString() != "explicit" {
		t.Fatalf("configured slug = %q, %v; want the block value", slug.ValueString(), ok)
	}
	if slug, ok := d.enterpriseSlugOr(types.StringNull()); !ok || slug.ValueString() != "provider-enterprise" {
		t.Fatalf("inherited slug = %q, %v; want the provider value", slug.ValueString(), ok)
	}
	if _, ok := (&configuredDataSource{}).enterpriseSlugOr(types.StringNull()); ok {
		t.Fatal("resolved a slug with no provider default")
	}
}
