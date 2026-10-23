package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const missingSlug = "Set enterprise_slug on the provider, the GITHUB_ENTERPRISE_SLUG environment variable, or on the block."

// applyEnterpriseSlugDefault fills the provider-level slug into the plan when
// the block omits it, so the planned value stays known instead of surfacing as
// a perpetual diff. Attribute plan modifiers run before this method, so a
// changed inherited slug has to request the replacement here.
func applyEnterpriseSlugDefault(ctx context.Context, providerSlug string, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if resp.Plan.Raw.IsNull() {
		return
	}
	var configured types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("enterprise_slug"), &configured)...)
	if resp.Diagnostics.HasError() || !configured.IsNull() {
		return
	}
	if providerSlug == "" {
		resp.Diagnostics.AddAttributeError(path.Root("enterprise_slug"), "Missing enterprise slug", missingSlug)
		return
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("enterprise_slug"), types.StringValue(providerSlug))...)
	if resp.Diagnostics.HasError() || req.State.Raw.IsNull() {
		return
	}
	var prior types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("enterprise_slug"), &prior)...)
	if resp.Diagnostics.HasError() || prior.IsNull() || prior.ValueString() == providerSlug {
		return
	}
	// A different enterprise means a different remote object, never an update.
	resp.RequiresReplace = append(resp.RequiresReplace, path.Root("enterprise_slug"))
}

// enterpriseSlugOr resolves a data source slug; data sources have no plan, so
// the fallback happens while reading.
func (r *configuredDataSource) enterpriseSlugOr(configured types.String) (types.String, bool) {
	if !configured.IsNull() {
		return configured, true
	}
	if r.enterpriseSlug == "" {
		return configured, false
	}
	return types.StringValue(r.enterpriseSlug), true
}
