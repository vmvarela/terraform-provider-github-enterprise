package provider

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/google/go-github/v93/github"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/vmvarela/terraform-provider-github-enterprise/internal/client"
)

var (
	_ resource.ResourceWithImportState = &organizationsResource{}
	_ resource.ResourceWithModifyPlan  = &organizationsResource{}
)

type organizationsResource struct{ configuredResource }
type organizationsModel struct {
	ID            types.String `tfsdk:"id"`
	Enterprise    types.String `tfsdk:"enterprise_slug"`
	TeamID        types.Int64  `tfsdk:"team_id"`
	Organizations types.Set    `tfsdk:"organization_slugs"`
}

func (r *organizationsResource) Metadata(_ context.Context, _ resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = resourceTypePrefix + "_team_organizations"
}
func (r *organizationsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Authoritatively manages all organization assignments for a team in selected mode. Omitted organizations are removed. Use only one instance per team; import existing assignments first. Creation and updates require GitHub to return organization_selection_type so selected mode can be verified.", Attributes: map[string]schema.Attribute{
		"id":                 schema.StringAttribute{Computed: true, MarkdownDescription: "enterprise_slug:team_id.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"enterprise_slug":    schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Enterprise account slug. Defaults to the provider-level enterprise_slug.", Validators: []validator.String{enterpriseRule()}, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"team_id":            schema.Int64Attribute{Required: true, MarkdownDescription: "Numeric team ID.", Validators: []validator.Int64{positiveID{}}, PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
		"organization_slugs": schema.SetAttribute{Required: true, ElementType: types.StringType, MarkdownDescription: "Complete set of lowercase organization slugs. An empty set removes all assignments.", Validators: []validator.Set{organizationSetRule{}}},
	}}
}

type organizationSetRule struct{}

func (organizationSetRule) Description(context.Context) string {
	return "Organization slugs must be lowercase letters, digits or hyphens, with no blank values."
}
func (v organizationSetRule) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (v organizationSetRule) ValidateSet(ctx context.Context, req validator.SetRequest, resp *validator.SetResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	for _, element := range req.ConfigValue.Elements() {
		s, ok := element.(types.String)
		if !ok || s.IsUnknown() {
			continue
		}
		check := enterpriseRule()
		var result validator.StringResponse
		check.ValidateString(ctx, validator.StringRequest{ConfigValue: s, Path: req.Path}, &result)
		if s.IsNull() || result.Diagnostics.HasError() {
			resp.Diagnostics.AddAttributeError(req.Path, "Invalid organization slug", v.Description(ctx))
			return
		}
	}
}

func organizationNames(orgs []*github.Organization) []string {
	names := make([]string, 0, len(orgs))
	for _, o := range orgs {
		names = append(names, strings.ToLower(o.GetLogin()))
	}
	sort.Strings(names)
	return names
}
func differences(current, desired []string) (add, remove []string) {
	c, d := map[string]bool{}, map[string]bool{}
	for _, s := range current {
		c[s] = true
	}
	for _, s := range desired {
		d[s] = true
	}
	for s := range d {
		if !c[s] {
			add = append(add, s)
		}
	}
	for s := range c {
		if !d[s] {
			remove = append(remove, s)
		}
	}
	sort.Strings(add)
	sort.Strings(remove)
	return
}
func (r *organizationsResource) selectedTeam(ctx context.Context, data organizationsModel) error {
	t, err := r.client.FindTeam(ctx, data.Enterprise.ValueString(), data.TeamID.ValueInt64(), "")
	if err != nil {
		return err
	}
	if t.OrganizationSelectionType == nil {
		return fmt.Errorf("GitHub did not return organization_selection_type for team %d; cannot safely manage assignments without verifying selected mode", t.ID)
	}
	if *t.OrganizationSelectionType != "selected" {
		return fmt.Errorf("team %d must have organization_selection_type = selected; current mode is %q", t.ID, *t.OrganizationSelectionType)
	}
	return nil
}
func (r *organizationsResource) reconcile(ctx context.Context, data organizationsModel, current []string) error {
	var desired []string
	if ds := data.Organizations.ElementsAs(ctx, &desired, false); ds.HasError() {
		return fmt.Errorf("invalid organization_slugs")
	}
	add, remove := differences(current, desired)
	if err := r.client.ChangeOrganizations(ctx, data.Enterprise.ValueString(), data.TeamID.ValueInt64(), "add", add); err != nil {
		return err
	}
	return r.client.ChangeOrganizations(ctx, data.Enterprise.ValueString(), data.TeamID.ValueInt64(), "remove", remove)
}
func (r *organizationsResource) readOrganizations(ctx context.Context, data *organizationsModel) diag.Diagnostics {
	orgs, err := r.client.Organizations(ctx, data.Enterprise.ValueString(), data.TeamID.ValueInt64())
	if err != nil {
		return diag.Diagnostics{diag.NewErrorDiagnostic("Read enterprise team organizations", err.Error())}
	}
	var ds diag.Diagnostics
	data.Organizations, ds = types.SetValueFrom(ctx, types.StringType, organizationNames(orgs))
	return ds
}
func (r *organizationsResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	applyEnterpriseSlugDefault(ctx, r.enterpriseSlug, req, resp)
}
func (r *organizationsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data organizationsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.selectedTeam(ctx, data); err != nil {
		resp.Diagnostics.AddError("Check enterprise team mode", err.Error())
		return
	}
	orgs, err := r.client.Organizations(ctx, data.Enterprise.ValueString(), data.TeamID.ValueInt64())
	if err != nil {
		resp.Diagnostics.AddError("Read existing organization assignments", err.Error())
		return
	}
	if len(orgs) > 0 {
		resp.Diagnostics.AddError("Existing organization assignments", "Import this team's assignments before managing them. Creation will not overwrite existing assignments.")
		return
	}
	data.ID = types.StringValue(joinID(data.Enterprise.ValueString(), strconv.FormatInt(data.TeamID.ValueInt64(), 10)))
	if err := r.reconcile(ctx, data, nil); err != nil {
		// An API request may have partially applied. Preserve the relation and
		// observed assignments so the next plan can recover without re-import.
		resp.Diagnostics.AddError("Assign enterprise team organizations", err.Error())
		resp.Diagnostics.Append(r.readOrganizations(ctx, &data)...)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
func (r *organizationsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data organizationsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	orgs, err := r.client.Organizations(ctx, data.Enterprise.ValueString(), data.TeamID.ValueInt64())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Read enterprise team organizations", err.Error())
		return
	}
	var ds diag.Diagnostics
	data.Organizations, ds = types.SetValueFrom(ctx, types.StringType, organizationNames(orgs))
	resp.Diagnostics.Append(ds...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
func (r *organizationsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data organizationsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.selectedTeam(ctx, data); err != nil {
		resp.Diagnostics.AddError("Check enterprise team mode", err.Error())
		return
	}
	orgs, err := r.client.Organizations(ctx, data.Enterprise.ValueString(), data.TeamID.ValueInt64())
	if err != nil {
		resp.Diagnostics.AddError("Read existing organization assignments", err.Error())
		return
	}
	if err := r.reconcile(ctx, data, organizationNames(orgs)); err != nil {
		resp.Diagnostics.AddError("Update enterprise team organizations", err.Error())
		resp.Diagnostics.Append(r.readOrganizations(ctx, &data)...)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
func (r *organizationsResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data organizationsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	orgs, err := r.client.Organizations(ctx, data.Enterprise.ValueString(), data.TeamID.ValueInt64())
	if client.IsNotFound(err) {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Read enterprise team organizations", err.Error())
		return
	}
	err = r.client.ChangeOrganizations(ctx, data.Enterprise.ValueString(), data.TeamID.ValueInt64(), "remove", organizationNames(orgs))
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Remove enterprise team organizations", err.Error())
	}
}
func (r *organizationsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts, err := parseImportID(req.ID, 2)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	id, _ := strconv.ParseInt(parts[1], 10, 64)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("enterprise_slug"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("team_id"), id)...)
}
