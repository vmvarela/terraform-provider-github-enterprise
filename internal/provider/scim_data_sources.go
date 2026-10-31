package provider

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/go-github/v93/github"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const scimDescription = "Reads existing Enterprise Managed Users SCIM identities. Read-only: it never provisions users or groups and never manages membership; the IdP remains the only SCIM writer."

var scimGroupTypes = map[string]attr.Type{"scim_group_id": types.StringType, "external_id": types.StringType, "display_name": types.StringType}
var scimUserTypes = map[string]attr.Type{"scim_user_id": types.StringType, "external_id": types.StringType, "display_name": types.StringType, "user_name": types.StringType, "active": types.BoolType}

func scimGroupAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"scim_group_id": schema.StringAttribute{Computed: true, MarkdownDescription: "Opaque, case-sensitive raw SCIM group ID. Use this value, not the data source's Terraform id, as a team's group_id."},
		"external_id":   schema.StringAttribute{Computed: true, MarkdownDescription: "IdP externalId, or null when omitted."},
		"display_name":  schema.StringAttribute{Computed: true, MarkdownDescription: "SCIM displayName, or null when omitted."},
	}
}
func scimUserAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"scim_user_id": schema.StringAttribute{Computed: true, MarkdownDescription: "Opaque, case-sensitive raw SCIM user ID."},
		"external_id":  schema.StringAttribute{Computed: true, MarkdownDescription: "IdP externalId, or null when omitted."},
		"display_name": schema.StringAttribute{Computed: true, MarkdownDescription: "SCIM displayName, or null when omitted."},
		"user_name":    schema.StringAttribute{Computed: true, MarkdownDescription: "SCIM userName, which is not necessarily the normalized GitHub login; null when omitted."},
		"active":       schema.BoolAttribute{Computed: true, MarkdownDescription: "SCIM active flag. An explicit false is preserved; go-github types this as a plain bool, so an omitted flag also reads as false."},
	}
}
func scimGroupObject(g *github.SCIMEnterpriseGroupAttributes) (types.Object, diag.Diagnostics) {
	return types.ObjectValue(scimGroupTypes, map[string]attr.Value{"scim_group_id": types.StringValue(strOrEmpty(g.ID)), "external_id": nullIfEmpty(g.ExternalID), "display_name": nullIfEmpty(g.DisplayName)})
}
func scimUserObject(u *github.SCIMEnterpriseUserAttributes) (types.Object, diag.Diagnostics) {
	return types.ObjectValue(scimUserTypes, map[string]attr.Value{"scim_user_id": types.StringValue(strOrEmpty(u.ID)), "external_id": nullIfEmpty(u.ExternalID), "display_name": nullIfEmpty(u.DisplayName), "user_name": nullIfEmpty(u.UserName), "active": types.BoolValue(u.Active)})
}

// scimQuote escapes a value for a SCIM filter, where double quotes delimit the
// string and backslash escapes.
func scimQuote(v string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(v, `\`, `\\`), `"`, `\"`) + `"`
}

// scimLookupFilter builds the filter for a single-attribute lookup.
func scimLookupFilter(attribute, value string) string {
	return attribute + " eq " + scimQuote(value)
}

// scimListID is deterministic and includes the escaped filter so distinct
// queries do not share an identity.
func scimListID(enterprise, kind, filter string) string {
	if filter == "" {
		return joinID(enterprise, kind)
	}
	return joinID(enterprise, kind) + "?filter=" + url.QueryEscape(filter)
}

type scimGroupDataSource struct{ configuredDataSource }
type scimGroupDataModel struct {
	ID          types.String `tfsdk:"id"`
	Enterprise  types.String `tfsdk:"enterprise_slug"`
	SCIMGroupID types.String `tfsdk:"scim_group_id"`
	ExternalID  types.String `tfsdk:"external_id"`
	DisplayName types.String `tfsdk:"display_name"`
}

func (d *scimGroupDataSource) Metadata(_ context.Context, _ datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = resourceTypePrefix + "_scim_group"
}
func (d *scimGroupDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	a := scimGroupAttributes()
	a["scim_group_id"] = schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Opaque, case-sensitive SCIM group ID from GitHub. Use this raw value, not the data source's Terraform id, as a team's group_id. Set exactly one of scim_group_id, display_name or external_id.", Validators: []validator.String{scimIDRule()}}
	a["display_name"] = schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "SCIM displayName to look the group up by. Fails when more than one group matches, so an ambiguous IdP name never picks a group silently.", Validators: []validator.String{nameRule()}}
	a["external_id"] = schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "IdP externalId to look the group up by. Fails when more than one group matches."}
	a["enterprise_slug"] = enterpriseDataAttribute()
	a["id"] = schema.StringAttribute{Computed: true, MarkdownDescription: "enterprise_slug:raw SCIM group ID."}
	resp.Schema = schema.Schema{MarkdownDescription: scimDescription + " Look the group up by scim_group_id, display_name or external_id; a name lookup fails unless it matches exactly one group.", Attributes: a}
}
func (d *scimGroupDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data scimGroupDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	enterprise, ok := d.enterpriseSlugOr(data.Enterprise)
	if !ok {
		resp.Diagnostics.AddAttributeError(path.Root("enterprise_slug"), "Missing enterprise slug", missingSlug)
		return
	}
	data.Enterprise = enterprise
	g, err := d.lookupGroup(ctx, enterprise.ValueString(), &data)
	if err != nil {
		resp.Diagnostics.AddError("Read SCIM group", err.Error())
		return
	}
	data.SCIMGroupID = types.StringValue(strOrEmpty(g.ID))
	data.ID = types.StringValue(joinID(enterprise.ValueString(), strOrEmpty(g.ID)))
	data.ExternalID, data.DisplayName = nullIfEmpty(g.ExternalID), nullIfEmpty(g.DisplayName)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// lookupGroup resolves the single lookup key the configuration set.
func (d *scimGroupDataSource) lookupGroup(ctx context.Context, enterprise string, data *scimGroupDataModel) (*github.SCIMEnterpriseGroupAttributes, error) {
	keys := []string{}
	if !data.SCIMGroupID.IsNull() && data.SCIMGroupID.ValueString() != "" {
		keys = append(keys, "scim_group_id")
	}
	if !data.DisplayName.IsNull() && data.DisplayName.ValueString() != "" {
		keys = append(keys, "display_name")
	}
	if !data.ExternalID.IsNull() && data.ExternalID.ValueString() != "" {
		keys = append(keys, "external_id")
	}
	if len(keys) != 1 {
		return nil, fmt.Errorf("set exactly one of scim_group_id, display_name or external_id; got %d", len(keys))
	}
	if keys[0] == "scim_group_id" {
		return d.client.SCIMGroup(ctx, enterprise, data.SCIMGroupID.ValueString())
	}
	attribute, value := "displayName", data.DisplayName.ValueString()
	if keys[0] == "external_id" {
		attribute, value = "externalId", data.ExternalID.ValueString()
	}
	groups, err := d.client.SCIMGroups(ctx, enterprise, scimLookupFilter(attribute, value))
	if err != nil {
		return nil, err
	}
	switch len(groups) {
	case 0:
		return nil, fmt.Errorf("no SCIM group matches %s %s", keys[0], value)
	case 1:
		return groups[0], nil
	default:
		return nil, fmt.Errorf("%d SCIM groups match %s %s; look the group up by scim_group_id instead", len(groups), keys[0], value)
	}
}

type scimGroupsDataSource struct{ configuredDataSource }
type scimGroupsDataModel struct {
	ID         types.String `tfsdk:"id"`
	Enterprise types.String `tfsdk:"enterprise_slug"`
	Filter     types.String `tfsdk:"filter"`
	Groups     types.List   `tfsdk:"groups"`
}

func (d *scimGroupsDataSource) Metadata(_ context.Context, _ datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = resourceTypePrefix + "_scim_groups"
}
func (d *scimGroupsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: scimDescription + " Lists follow SCIM startIndex/count pagination; an empty result is an empty list.", Attributes: map[string]schema.Attribute{
		"id":              schema.StringAttribute{Computed: true, MarkdownDescription: "Deterministic query identity: enterprise_slug:groups, plus the escaped filter when set."},
		"enterprise_slug": enterpriseDataAttribute(),
		"filter":          schema.StringAttribute{Optional: true, MarkdownDescription: "One SCIM filter passed to GitHub unchanged. Supports group externalId, id and displayName; multiple filters are not supported."},
		"groups":          schema.ListNestedAttribute{Computed: true, MarkdownDescription: "SCIM groups in API order. Each entry has the raw scim_group_id (for team group_id), IdP external_id and display_name, null when empty.", NestedObject: schema.NestedAttributeObject{Attributes: scimGroupAttributes()}},
	}}
}
func (d *scimGroupsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data scimGroupsDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	enterprise, ok := d.enterpriseSlugOr(data.Enterprise)
	if !ok {
		resp.Diagnostics.AddAttributeError(path.Root("enterprise_slug"), "Missing enterprise slug", missingSlug)
		return
	}
	data.Enterprise = enterprise
	filter := ""
	if !data.Filter.IsNull() {
		filter = data.Filter.ValueString()
	}
	groups, err := d.client.SCIMGroups(ctx, enterprise.ValueString(), filter)
	if err != nil {
		resp.Diagnostics.AddError("List SCIM groups", err.Error())
		return
	}
	values := make([]attr.Value, 0, len(groups))
	for _, g := range groups {
		v, ds := scimGroupObject(g)
		resp.Diagnostics.Append(ds...)
		values = append(values, v)
	}
	var ds diag.Diagnostics
	data.Groups, ds = types.ListValue(types.ObjectType{AttrTypes: scimGroupTypes}, values)
	resp.Diagnostics.Append(ds...)
	data.ID = types.StringValue(scimListID(enterprise.ValueString(), "groups", filter))
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

type scimUserDataSource struct{ configuredDataSource }
type scimUserDataModel struct {
	ID          types.String `tfsdk:"id"`
	Enterprise  types.String `tfsdk:"enterprise_slug"`
	SCIMUserID  types.String `tfsdk:"scim_user_id"`
	ExternalID  types.String `tfsdk:"external_id"`
	DisplayName types.String `tfsdk:"display_name"`
	UserName    types.String `tfsdk:"user_name"`
	Active      types.Bool   `tfsdk:"active"`
}

func (d *scimUserDataSource) Metadata(_ context.Context, _ datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = resourceTypePrefix + "_scim_user"
}
func (d *scimUserDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	a := scimUserAttributes()
	a["scim_user_id"] = schema.StringAttribute{Required: true, MarkdownDescription: "Opaque, case-sensitive SCIM user ID from GitHub.", Validators: []validator.String{scimIDRule()}}
	a["enterprise_slug"] = enterpriseDataAttribute()
	a["id"] = schema.StringAttribute{Computed: true, MarkdownDescription: "enterprise_slug:raw SCIM user ID."}
	resp.Schema = schema.Schema{MarkdownDescription: scimDescription, Attributes: a}
}
func (d *scimUserDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data scimUserDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	enterprise, ok := d.enterpriseSlugOr(data.Enterprise)
	if !ok {
		resp.Diagnostics.AddAttributeError(path.Root("enterprise_slug"), "Missing enterprise slug", missingSlug)
		return
	}
	data.Enterprise = enterprise
	u, err := d.client.SCIMUser(ctx, enterprise.ValueString(), data.SCIMUserID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Read SCIM user", err.Error())
		return
	}
	data.ID = types.StringValue(joinID(enterprise.ValueString(), strOrEmpty(u.ID)))
	data.ExternalID, data.DisplayName, data.UserName, data.Active = nullIfEmpty(u.ExternalID), nullIfEmpty(u.DisplayName), nullIfEmpty(u.UserName), types.BoolValue(u.Active)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

type scimUsersDataSource struct{ configuredDataSource }
type scimUsersDataModel struct {
	ID         types.String `tfsdk:"id"`
	Enterprise types.String `tfsdk:"enterprise_slug"`
	Filter     types.String `tfsdk:"filter"`
	Users      types.List   `tfsdk:"users"`
}

func (d *scimUsersDataSource) Metadata(_ context.Context, _ datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = resourceTypePrefix + "_scim_users"
}
func (d *scimUsersDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: scimDescription + " Lists follow SCIM startIndex/count pagination and include soft-deprovisioned users. An empty result is an empty list.", Attributes: map[string]schema.Attribute{
		"id":              schema.StringAttribute{Computed: true, MarkdownDescription: "Deterministic query identity: enterprise_slug:users, plus the escaped filter when set."},
		"enterprise_slug": enterpriseDataAttribute(),
		"filter":          schema.StringAttribute{Optional: true, MarkdownDescription: "One SCIM filter passed to GitHub unchanged. Supports user externalId, id, displayName and userName; active filters and multiple filters are not supported."},
		"users":           schema.ListNestedAttribute{Computed: true, MarkdownDescription: "SCIM users in API order. Each entry has the raw scim_user_id, IdP external_id, display_name, user_name (not necessarily the GitHub login) and active flag; empty strings read as null. Explicit active=false is preserved.", NestedObject: schema.NestedAttributeObject{Attributes: scimUserAttributes()}},
	}}
}
func (d *scimUsersDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data scimUsersDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	enterprise, ok := d.enterpriseSlugOr(data.Enterprise)
	if !ok {
		resp.Diagnostics.AddAttributeError(path.Root("enterprise_slug"), "Missing enterprise slug", missingSlug)
		return
	}
	data.Enterprise = enterprise
	filter := ""
	if !data.Filter.IsNull() {
		filter = data.Filter.ValueString()
	}
	users, err := d.client.SCIMUsers(ctx, enterprise.ValueString(), filter)
	if err != nil {
		resp.Diagnostics.AddError("List SCIM users", err.Error())
		return
	}
	values := make([]attr.Value, 0, len(users))
	for _, u := range users {
		v, ds := scimUserObject(u)
		resp.Diagnostics.Append(ds...)
		values = append(values, v)
	}
	var ds diag.Diagnostics
	data.Users, ds = types.ListValue(types.ObjectType{AttrTypes: scimUserTypes}, values)
	resp.Diagnostics.Append(ds...)
	data.ID = types.StringValue(scimListID(enterprise.ValueString(), "users", filter))
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
