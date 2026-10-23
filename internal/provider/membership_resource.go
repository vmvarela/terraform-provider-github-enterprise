package provider

import (
	"context"
	"strconv"

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
	_ resource.ResourceWithImportState = &membershipResource{}
	_ resource.ResourceWithModifyPlan  = &membershipResource{}
)

type membershipResource struct{ configuredResource }
type membershipModel struct {
	ID         types.String `tfsdk:"id"`
	Enterprise types.String `tfsdk:"enterprise_slug"`
	TeamID     types.Int64  `tfsdk:"team_id"`
	Username   types.String `tfsdk:"username"`
	UserID     types.Int64  `tfsdk:"user_id"`
}

func (r *membershipResource) Metadata(_ context.Context, _ resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = resourceTypePrefix + "_team_membership"
}
func (r *membershipResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Manages one direct team membership. Other members are unaffected. Do not use with IdP-managed team memberships.", Attributes: map[string]schema.Attribute{
		"id":              schema.StringAttribute{Computed: true, MarkdownDescription: "enterprise_slug/team_id/username.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"enterprise_slug": schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Enterprise account slug. Defaults to the provider-level enterprise_slug.", Validators: []validator.String{enterpriseRule()}, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"team_id":         schema.Int64Attribute{Required: true, MarkdownDescription: "Numeric team ID. Remains stable when a team is renamed.", Validators: []validator.Int64{positiveID{}}, PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
		"username":        schema.StringAttribute{Required: true, MarkdownDescription: "GitHub username.", Validators: []validator.String{usernameRule()}, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"user_id":         schema.Int64Attribute{Computed: true, MarkdownDescription: "Numeric GitHub user ID."},
	}}
}
func (r *membershipResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	applyEnterpriseSlugDefault(ctx, r.enterpriseSlug, req, resp)
}
func (r *membershipResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data membershipModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	u, err := r.client.AddMember(ctx, data.Enterprise.ValueString(), data.TeamID.ValueInt64(), data.Username.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Add enterprise team member", err.Error())
		return
	}
	data.ID = types.StringValue(data.Enterprise.ValueString() + "/" + strconv.FormatInt(data.TeamID.ValueInt64(), 10) + "/" + data.Username.ValueString())
	data.UserID = types.Int64Value(u.ID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
func (r *membershipResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data membershipModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	u, err := r.client.Membership(ctx, data.Enterprise.ValueString(), data.TeamID.ValueInt64(), data.Username.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Read enterprise team membership", err.Error())
		return
	}
	data.UserID = types.Int64Value(u.ID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
func (r *membershipResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Unexpected membership update", "Changing the enterprise, team or username requires replacement.")
}
func (r *membershipResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data membershipModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client.RemoveMember(ctx, data.Enterprise.ValueString(), data.TeamID.ValueInt64(), data.Username.ValueString())
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Remove enterprise team member", err.Error())
	}
}
func (r *membershipResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts, err := parseImportID(req.ID, 3)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	id, _ := strconv.ParseInt(parts[1], 10, 64)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("enterprise_slug"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("team_id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("username"), parts[2])...)
}
