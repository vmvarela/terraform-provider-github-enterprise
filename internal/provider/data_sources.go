package provider

import (
	"context"
	"fmt"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func enterpriseDataAttribute() schema.StringAttribute {
	return schema.StringAttribute{Required: true, MarkdownDescription: "Enterprise account slug.", Validators: []validator.String{enterpriseRule()}}
}
func teamIDDataAttribute() schema.Int64Attribute {
	return schema.Int64Attribute{Required: true, MarkdownDescription: "Numeric enterprise team ID.", Validators: []validator.Int64{positiveID{}}}
}
func teamDataAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"team_id":                     schema.Int64Attribute{Computed: true, MarkdownDescription: "Numeric team ID."},
		"name":                        schema.StringAttribute{Computed: true, MarkdownDescription: "Team name."},
		"slug":                        schema.StringAttribute{Computed: true, MarkdownDescription: "GitHub-generated slug."},
		"description":                 schema.StringAttribute{Computed: true, MarkdownDescription: "Team description."},
		"group_id":                    schema.StringAttribute{Computed: true, MarkdownDescription: "Linked IdP group ID, or an empty string."},
		"organization_selection_type": schema.StringAttribute{Computed: true, MarkdownDescription: "Organization selection mode."},
		"notification_setting":        schema.StringAttribute{Computed: true, MarkdownDescription: "Mention notification mode."},
	}
}

type teamDataSource struct{ configuredDataSource }

func (d *teamDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_team"
}
func (d *teamDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	a := teamDataAttributes()
	a["enterprise_slug"] = enterpriseDataAttribute()
	a["id"] = schema.StringAttribute{Computed: true, MarkdownDescription: "enterprise_slug/team_id."}
	a["slug"] = schema.StringAttribute{Required: true, MarkdownDescription: "Team slug returned by GitHub, including ent: when present.", Validators: []validator.String{slugRule()}}
	resp.Schema = schema.Schema{MarkdownDescription: "Looks up an enterprise team by slug.", Attributes: a}
}
func (d *teamDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data teamModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	t, err := d.client.TeamBySlug(ctx, data.Enterprise.ValueString(), data.Slug.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Read enterprise team", err.Error())
		return
	}
	requested := data.Slug
	data.refresh(t)
	// A required data-source input must retain the user's configured value.
	if !requested.Equal(data.Slug) {
		resp.Diagnostics.AddError("Unexpected team slug", fmt.Sprintf("GitHub returned slug %q for %q; use the canonical slug", t.Slug, requested.ValueString()))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

type teamsDataSource struct{ configuredDataSource }
type teamsModel struct {
	ID         types.String `tfsdk:"id"`
	Enterprise types.String `tfsdk:"enterprise_slug"`
	Teams      types.List   `tfsdk:"teams"`
}

func (d *teamsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_teams"
}
func (d *teamsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Lists all enterprise teams, following every pagination page.", Attributes: map[string]schema.Attribute{
		"id":              schema.StringAttribute{Computed: true, MarkdownDescription: "Enterprise slug."},
		"enterprise_slug": enterpriseDataAttribute(),
		"teams":           schema.ListNestedAttribute{Computed: true, MarkdownDescription: "Enterprise teams in API order.", NestedObject: schema.NestedAttributeObject{Attributes: teamDataAttributes()}},
	}}
}
func (d *teamsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data teamsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	teams, err := d.client.Teams(ctx, data.Enterprise.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("List enterprise teams", err.Error())
		return
	}
	objectTypes := map[string]attr.Type{"team_id": types.Int64Type, "name": types.StringType, "slug": types.StringType, "description": types.StringType, "group_id": types.StringType, "organization_selection_type": types.StringType, "notification_setting": types.StringType}
	values := make([]attr.Value, 0, len(teams))
	for _, t := range teams {
		selection := t.OrganizationSelectionType
		if selection == "" {
			selection = "disabled"
		}
		notifications := t.NotificationSetting
		if notifications == "" {
			notifications = "notifications_enabled"
		}
		v, ds := types.ObjectValue(objectTypes, map[string]attr.Value{"team_id": types.Int64Value(t.ID), "name": types.StringValue(t.Name), "slug": types.StringValue(t.Slug), "description": types.StringValue(t.Description), "group_id": types.StringValue(t.GroupID), "organization_selection_type": types.StringValue(selection), "notification_setting": types.StringValue(notifications)})
		resp.Diagnostics.Append(ds...)
		values = append(values, v)
	}
	list, ds := types.ListValue(types.ObjectType{AttrTypes: objectTypes}, values)
	data.Teams = list
	resp.Diagnostics.Append(ds...)
	data.ID = data.Enterprise
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

type membershipDataSource struct{ configuredDataSource }
type membershipDataModel struct {
	ID         types.String `tfsdk:"id"`
	Enterprise types.String `tfsdk:"enterprise_slug"`
	TeamID     types.Int64  `tfsdk:"team_id"`
	Username   types.String `tfsdk:"username"`
	UserID     types.Int64  `tfsdk:"user_id"`
	Member     types.Bool   `tfsdk:"is_member"`
}

func (d *membershipDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_team_membership"
}
func (d *membershipDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Looks up a direct or IdP-managed team membership. Missing memberships produce an error; authorization errors are never reported as non-membership.", Attributes: map[string]schema.Attribute{
		"id":              schema.StringAttribute{Computed: true, MarkdownDescription: "enterprise_slug/team_id/username."},
		"enterprise_slug": enterpriseDataAttribute(), "team_id": teamIDDataAttribute(),
		"username":  schema.StringAttribute{Required: true, MarkdownDescription: "GitHub username.", Validators: []validator.String{usernameRule()}},
		"user_id":   schema.Int64Attribute{Computed: true, MarkdownDescription: "Numeric user ID."},
		"is_member": schema.BoolAttribute{Computed: true, MarkdownDescription: "True when the membership exists. A missing membership is an error."},
	}}
}
func (d *membershipDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data membershipDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	u, err := d.client.Membership(ctx, data.Enterprise.ValueString(), data.TeamID.ValueInt64(), data.Username.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Read enterprise team membership", err.Error())
		return
	}
	data.ID = types.StringValue(data.Enterprise.ValueString() + "/" + strconv.FormatInt(data.TeamID.ValueInt64(), 10) + "/" + data.Username.ValueString())
	data.UserID = types.Int64Value(u.ID)
	data.Member = types.BoolValue(true)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

type organizationsDataSource struct{ configuredDataSource }

func (d *organizationsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_team_organizations"
}
func (d *organizationsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Lists all organization assignments for an enterprise team.", Attributes: map[string]schema.Attribute{
		"id": schema.StringAttribute{Computed: true, MarkdownDescription: "enterprise_slug/team_id."}, "enterprise_slug": enterpriseDataAttribute(), "team_id": teamIDDataAttribute(),
		"organization_slugs": schema.SetAttribute{Computed: true, ElementType: types.StringType, MarkdownDescription: "Assigned organization slugs."},
	}}
}
func (d *organizationsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data organizationsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	orgs, err := d.client.Organizations(ctx, data.Enterprise.ValueString(), data.TeamID.ValueInt64())
	if err != nil {
		resp.Diagnostics.AddError("Read enterprise team organizations", err.Error())
		return
	}
	values, ds := types.SetValueFrom(ctx, types.StringType, organizationNames(orgs))
	resp.Diagnostics.Append(ds...)
	data.Organizations = values
	data.ID = types.StringValue(data.Enterprise.ValueString() + "/" + strconv.FormatInt(data.TeamID.ValueInt64(), 10))
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
