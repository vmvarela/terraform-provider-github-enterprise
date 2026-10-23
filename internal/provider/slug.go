package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const missingSlug = "Set enterprise_slug on the provider, the GITHUB_ENTERPRISE_SLUG environment variable, or on the block."

// applyEnterpriseSlugDefault writes the provider-level slug into the plan when
// the block omits it, so the planned value stays known instead of surfacing as
// a perpetual diff.
func applyEnterpriseSlugDefault(ctx context.Context, slug string, cfg tfsdk.Config, resp *resource.ModifyPlanResponse) {
	if resp.Plan.Raw.IsNull() {
		return
	}
	var configured types.String
	resp.Diagnostics.Append(cfg.GetAttribute(ctx, path.Root("enterprise_slug"), &configured)...)
	if resp.Diagnostics.HasError() || !configured.IsNull() {
		return
	}
	if slug == "" {
		resp.Diagnostics.AddAttributeError(path.Root("enterprise_slug"), "Missing enterprise slug", missingSlug)
		return
	}
	// The framework keeps the modified plan in the response, not the request.
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("enterprise_slug"), types.StringValue(slug))...)
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
