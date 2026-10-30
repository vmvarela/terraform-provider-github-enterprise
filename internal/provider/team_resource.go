package provider

import (
	"context"
	"strconv"

	"github.com/google/go-github/v93/github"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/vmvarela/terraform-provider-github-enterprise/internal/client"
)

var (
	_ resource.ResourceWithImportState = &teamResource{}
	_ resource.ResourceWithModifyPlan  = &teamResource{}
)

type teamResource struct{ configuredResource }
type teamModel struct {
	ID          types.String `tfsdk:"id"`
	Enterprise  types.String `tfsdk:"enterprise_slug"`
	TeamID      types.Int64  `tfsdk:"team_id"`
	Slug        types.String `tfsdk:"slug"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	GroupID     types.String `tfsdk:"group_id"`
	Selection   types.String `tfsdk:"organization_selection_type"`
}

func (r *teamResource) Metadata(_ context.Context, _ resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = resourceTypePrefix + "_team"
}
func (r *teamResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Manages a GitHub Enterprise Cloud team. Numeric identity is preserved across renames. Refresh and import fail without changing state if GitHub omits organization_selection_type; creation defaults are never treated as observed API values.", Attributes: map[string]schema.Attribute{
		"id":                          schema.StringAttribute{Computed: true, MarkdownDescription: "Stable identity: enterprise_slug/team_id.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"enterprise_slug":             schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Enterprise account slug. Defaults to the provider-level enterprise_slug.", Validators: []validator.String{enterpriseRule()}, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"team_id":                     schema.Int64Attribute{Computed: true, MarkdownDescription: "Stable numeric team ID. Use this in membership and organization assignment resources.", PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}},
		"slug":                        schema.StringAttribute{Computed: true, MarkdownDescription: "GitHub-generated team slug; can change when the team is renamed."},
		"name":                        schema.StringAttribute{Required: true, MarkdownDescription: "Team name.", Validators: []validator.String{nameRule()}},
		"description":                 schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(""), MarkdownDescription: "Team description. Removing it clears the remote description."},
		"group_id":                    schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(""), MarkdownDescription: "IdP group ID, taken from the scim_group or scim_groups data sources. An empty value unlinks the group by sending an explicit JSON null. Do not manage direct memberships for an IdP-linked team."},
		"organization_selection_type": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("disabled"), MarkdownDescription: "disabled, selected, or all. Use selected with the team_organizations resource. Changing this may revoke organization access.", Validators: []validator.String{oneOf("disabled", "selected", "all")}},
	}}
}

func (m *teamModel) identity(t *github.EnterpriseTeam) {
	m.TeamID = types.Int64Value(t.ID)
	m.ID = types.StringValue(m.Enterprise.ValueString() + "/" + strconv.FormatInt(t.ID, 10))
	m.Slug = types.StringValue(t.Slug)
}
func (m *teamModel) refresh(t *github.EnterpriseTeam) {
	m.identity(t)
	m.Name = types.StringValue(t.Name)
	m.Description = types.StringValue(strOrEmpty(t.Description))
	m.GroupID = types.StringValue(t.GroupID)
	m.Selection = types.StringPointerValue(t.OrganizationSelectionType)
}
func (m teamModel) body() github.EnterpriseTeamCreateOrUpdateRequest {
	body := github.EnterpriseTeamCreateOrUpdateRequest{
		Name:                      m.Name.ValueString(),
		Description:               github.String(m.Description.ValueString()),
		OrganizationSelectionType: github.String(m.Selection.ValueString()),
	}
	if m.GroupID.ValueString() != "" {
		body.GroupID = github.String(m.GroupID.ValueString())
	}
	return body
}

// updateBody stays a map: unlinking an IdP group needs an explicit JSON null,
// which the typed request cannot express.
func (m teamModel) updateBody() map[string]any {
	body := map[string]any{"name": m.Name.ValueString(), "description": m.Description.ValueString(), "organization_selection_type": m.Selection.ValueString()}
	if m.GroupID.ValueString() != "" {
		body["group_id"] = m.GroupID.ValueString()
	}
	return body
}

func (r *teamResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	applyEnterpriseSlugDefault(ctx, r.enterpriseSlug, req, resp)
}
func (r *teamResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data teamModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	t, err := r.client.CreateTeam(ctx, data.Enterprise.ValueString(), data.body())
	if err != nil {
		resp.Diagnostics.AddError("Create enterprise team", err.Error())
		return
	}
	data.identity(t)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
func (r *teamResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data teamModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	t, err := r.client.FindTeam(ctx, data.Enterprise.ValueString(), data.TeamID.ValueInt64(), data.Slug.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Read enterprise team", err.Error())
		return
	}
	if t.OrganizationSelectionType == nil {
		resp.Diagnostics.AddError("Incomplete enterprise team response", "GitHub did not return organization_selection_type. The team cannot be safely refreshed or imported without this setting. Existing state has been retained; verify API permissions and compatibility before retrying.")
		return
	}
	data.refresh(t)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
func (r *teamResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data, old teamModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
	if resp.Diagnostics.HasError() {
		return
	}
	t, err := r.client.FindTeam(ctx, old.Enterprise.ValueString(), old.TeamID.ValueInt64(), old.Slug.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Resolve enterprise team", err.Error())
		return
	}
	body := data.updateBody()
	// Never re-send group_id for unrelated updates: assigning it can replace
	// direct members. Explicit null is distinct from omitting the field.
	delete(body, "group_id")
	if !data.GroupID.Equal(old.GroupID) {
		if data.GroupID.ValueString() == "" {
			body["group_id"] = nil
		} else {
			body["group_id"] = data.GroupID.ValueString()
		}
	}
	updated, err := r.client.UpdateTeam(ctx, old.Enterprise.ValueString(), t.Slug, body)
	if err != nil {
		resp.Diagnostics.AddError("Update enterprise team", err.Error())
		return
	}
	if updated.ID != old.TeamID.ValueInt64() || updated.Slug == "" {
		resp.Diagnostics.AddError("Unexpected enterprise team identity", "GitHub returned an unexpected team identity; refresh state before retrying.")
		return
	}
	data.identity(updated)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
func (r *teamResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data teamModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	t, err := r.client.FindTeam(ctx, data.Enterprise.ValueString(), data.TeamID.ValueInt64(), data.Slug.ValueString())
	if client.IsNotFound(err) {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Resolve enterprise team", err.Error())
		return
	}
	err = r.client.DeleteTeam(ctx, data.Enterprise.ValueString(), t.Slug)
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Delete enterprise team", err.Error())
	}
}
func (r *teamResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
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
