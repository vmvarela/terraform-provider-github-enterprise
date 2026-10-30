package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/google/go-github/v93/github"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/vmvarela/terraform-provider-github-enterprise/internal/client"
)

var (
	_ resource.ResourceWithImportState = &costCenterResource{}
	_ resource.ResourceWithModifyPlan  = &costCenterResource{}
)

// errArchived marks a cost center GitHub reports as deleted (archived); callers treat it as absent.
var errArchived = errors.New("cost center is archived")

type costCenterResource struct{ configuredResource }
type costCenterModel struct {
	ID           types.String `tfsdk:"id"`
	Enterprise   types.String `tfsdk:"enterprise_slug"`
	CostCenterID types.String `tfsdk:"cost_center_id"`
	Name         types.String `tfsdk:"name"`
	State        types.String `tfsdk:"state"`
	Azure        types.String `tfsdk:"azure_subscription"`
}

// validCostCenterID treats the ID as an opaque string: it only has to be a
// single, safe URL path segment, not a UUID or number.
func validCostCenterID(s string) bool {
	if s == "" || s == "." || s == ".." || len(s) > 256 || strings.Contains(s, "/") {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}
func costCenterIDRule() validator.String {
	return stringRule{"Must be the opaque cost center ID returned by GitHub: a single path segment without slashes, whitespace or control characters.", validCostCenterID}
}
func parseCostCenterImportID(id string) (enterprise, costCenter string, err error) {
	parts := strings.Split(id, "/")
	if len(parts) != 2 || !enterpriseSlugPattern.MatchString(parts[0]) || !validCostCenterID(parts[1]) {
		return "", "", fmt.Errorf("expected enterprise_slug/cost_center_id")
	}
	return parts[0], parts[1], nil
}
func costCenterArchived(cc *github.CostCenter) bool {
	return strings.EqualFold(strOrEmpty(cc.State), "deleted")
}

// strOrEmpty reads an optional SDK string as the empty string when absent.
func strOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// nullIfEmpty keeps plain SDK strings nullable in Terraform. For SCIM an empty
// value may also mean the IdP omitted the attribute.
func nullIfEmpty(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}

func (m *costCenterModel) refresh(cc *github.CostCenter) {
	m.ID = types.StringValue(m.Enterprise.ValueString() + "/" + cc.ID)
	m.CostCenterID = types.StringValue(cc.ID)
	m.Name = types.StringValue(cc.Name)
	m.State = nullIfEmpty(strOrEmpty(cc.State))
	m.Azure = types.StringPointerValue(cc.AzureSubscription)
}

func (r *costCenterResource) Metadata(_ context.Context, _ resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = resourceTypePrefix + "_cost_center"
}
func (r *costCenterResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Manages a GitHub Enterprise Cloud cost center name. The cost center keeps its opaque ID across renames. This resource does not own assignments; use `github_enterprise_cost_center_resources` for those. **Destroying it archives the cost center** (GitHub has no hard delete), which releases its assignments. Requires an enterprise billing manager or enterprise owner token.", Attributes: map[string]schema.Attribute{
		"id":                 schema.StringAttribute{Computed: true, MarkdownDescription: "Stable identity: enterprise_slug/cost_center_id.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"enterprise_slug":    schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Enterprise account slug. Defaults to the provider-level enterprise_slug. Changing it replaces the resource.", Validators: []validator.String{enterpriseRule()}, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"cost_center_id":     schema.StringAttribute{Computed: true, MarkdownDescription: "Opaque cost center ID assigned by GitHub.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"name":               schema.StringAttribute{Required: true, MarkdownDescription: "Cost center name.", Validators: []validator.String{nameRule()}},
		"state":              schema.StringAttribute{Computed: true, MarkdownDescription: "State reported by GitHub, or null when GitHub omits it."},
		"azure_subscription": schema.StringAttribute{Computed: true, MarkdownDescription: "Azure subscription ID linked by GitHub, or null. Read-only."},
	}}
}
func (r *costCenterResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	applyEnterpriseSlugDefault(ctx, r.enterpriseSlug, req, resp)
}
func (r *costCenterResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data costCenterModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	cc, err := r.client.CreateCostCenter(ctx, data.Enterprise.ValueString(), data.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Create cost center", err.Error())
		// A known ID is persisted with the planned name (last known, not observed)
		// so the next apply cannot orphan or duplicate it; Read corrects it.
		if cc != nil && validCostCenterID(cc.ID) {
			data.ID = types.StringValue(data.Enterprise.ValueString() + "/" + cc.ID)
			data.CostCenterID = types.StringValue(cc.ID)
			data.State, data.Azure = nullIfEmpty(strOrEmpty(cc.State)), types.StringPointerValue(cc.AzureSubscription)
			resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
		}
		return
	}
	data.refresh(cc)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
func (r *costCenterResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data costCenterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	cc, err := r.client.CostCenter(ctx, data.Enterprise.ValueString(), data.CostCenterID.ValueString())
	if client.IsNotFound(err) || (err == nil && costCenterArchived(cc)) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Read cost center", err.Error())
		return
	}
	data.refresh(cc)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
func (r *costCenterResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data, old costCenterModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
	if resp.Diagnostics.HasError() {
		return
	}
	cc, err := r.client.UpdateCostCenter(ctx, old.Enterprise.ValueString(), old.CostCenterID.ValueString(), data.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Update cost center", err.Error())
		return
	}
	data.Enterprise = old.Enterprise
	data.refresh(cc)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
func (r *costCenterResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data costCenterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client.DeleteCostCenter(ctx, data.Enterprise.ValueString(), data.CostCenterID.ValueString())
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Archive cost center", err.Error())
	}
}
func (r *costCenterResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	enterprise, id, err := parseCostCenterImportID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("enterprise_slug"), enterprise)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("cost_center_id"), id)...)
}
