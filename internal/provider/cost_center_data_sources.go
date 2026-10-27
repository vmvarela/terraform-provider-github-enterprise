package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/vmvarela/terraform-provider-github-enterprise/internal/client"
)

var costCenterResourceTypes = map[string]attr.Type{"type": types.StringType, "name": types.StringType}
var costCenterTypes = map[string]attr.Type{"cost_center_id": types.StringType, "name": types.StringType, "state": types.StringType, "azure_subscription": types.StringType, "resources": types.ListType{ElemType: types.ObjectType{AttrTypes: costCenterResourceTypes}}}

const rawResourcesDescription = "Raw assignments as returned by GitHub, in API order. Type values are not interpreted, so unknown types are exposed as-is."

func costCenterDataAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"name":               schema.StringAttribute{Computed: true, MarkdownDescription: "Cost center name."},
		"state":              schema.StringAttribute{Computed: true, MarkdownDescription: "State reported by GitHub, or null when omitted."},
		"azure_subscription": schema.StringAttribute{Computed: true, MarkdownDescription: "Linked Azure subscription ID, or null."},
		"resources": schema.ListNestedAttribute{Computed: true, MarkdownDescription: rawResourcesDescription, NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"type": schema.StringAttribute{Computed: true, MarkdownDescription: "GitHub resource type, for example User, Org, Repo or Team."},
			"name": schema.StringAttribute{Computed: true, MarkdownDescription: "Resource identifier as returned by GitHub."},
		}}},
	}
}
func costCenterObject(cc client.CostCenter) (types.Object, diag.Diagnostics) {
	res := make([]attr.Value, 0, len(cc.Resources))
	var all diag.Diagnostics
	for _, r := range cc.Resources {
		v, ds := types.ObjectValue(costCenterResourceTypes, map[string]attr.Value{"type": types.StringValue(r.Type), "name": types.StringValue(r.Name)})
		all.Append(ds...)
		res = append(res, v)
	}
	list, ds := types.ListValue(types.ObjectType{AttrTypes: costCenterResourceTypes}, res)
	all.Append(ds...)
	obj, ds := types.ObjectValue(costCenterTypes, map[string]attr.Value{"cost_center_id": types.StringValue(cc.ID), "name": types.StringValue(cc.Name), "state": nullIfEmpty(cc.State), "azure_subscription": types.StringPointerValue(cc.AzureSubscription), "resources": list})
	all.Append(ds...)
	return obj, all
}

type costCenterDataSource struct{ configuredDataSource }
type costCenterDataModel struct {
	ID           types.String `tfsdk:"id"`
	Enterprise   types.String `tfsdk:"enterprise_slug"`
	CostCenterID types.String `tfsdk:"cost_center_id"`
	Name         types.String `tfsdk:"name"`
	State        types.String `tfsdk:"state"`
	Azure        types.String `tfsdk:"azure_subscription"`
	Resources    types.List   `tfsdk:"resources"`
}

func (d *costCenterDataSource) Metadata(_ context.Context, _ datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = resourceTypePrefix + "_cost_center"
}
func (d *costCenterDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	a := costCenterDataAttributes()
	a["id"] = schema.StringAttribute{Computed: true, MarkdownDescription: "enterprise_slug/cost_center_id."}
	a["enterprise_slug"] = enterpriseDataAttribute()
	a["cost_center_id"] = schema.StringAttribute{Required: true, MarkdownDescription: "Opaque cost center ID.", Validators: []validator.String{costCenterIDRule()}}
	resp.Schema = schema.Schema{MarkdownDescription: "Reads one cost center, including all of its assignments. Archived cost centers are returned with their reported state.", Attributes: a}
}
func (d *costCenterDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data costCenterDataModel
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
	cc, err := d.client.CostCenter(ctx, enterprise.ValueString(), data.CostCenterID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Read cost center", err.Error())
		return
	}
	obj, ds := costCenterObject(*cc)
	resp.Diagnostics.Append(ds...)
	if resp.Diagnostics.HasError() {
		return
	}
	a := obj.Attributes()
	data.ID = types.StringValue(enterprise.ValueString() + "/" + cc.ID)
	data.Name, data.State, data.Azure, data.Resources = a["name"].(types.String), a["state"].(types.String), a["azure_subscription"].(types.String), a["resources"].(types.List)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

type costCentersDataSource struct{ configuredDataSource }
type costCentersDataModel struct {
	ID          types.String `tfsdk:"id"`
	Enterprise  types.String `tfsdk:"enterprise_slug"`
	State       types.String `tfsdk:"state"`
	CostCenters types.List   `tfsdk:"cost_centers"`
}

func (d *costCentersDataSource) Metadata(_ context.Context, _ datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = resourceTypePrefix + "_cost_centers"
}
func (d *costCentersDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	a := costCenterDataAttributes()
	a["cost_center_id"] = schema.StringAttribute{Computed: true, MarkdownDescription: "Opaque cost center ID."}
	resp.Schema = schema.Schema{MarkdownDescription: "Lists cost centers with their assignments, following every pagination page.", Attributes: map[string]schema.Attribute{
		"id":              schema.StringAttribute{Computed: true, MarkdownDescription: "enterprise_slug/state."},
		"enterprise_slug": enterpriseDataAttribute(),
		"state":           schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Filter: active (default) or deleted.", Validators: []validator.String{oneOf("active", "deleted")}},
		"cost_centers":    schema.ListNestedAttribute{Computed: true, MarkdownDescription: "Cost centers in API order.", NestedObject: schema.NestedAttributeObject{Attributes: a}},
	}}
}
func (d *costCentersDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data costCentersDataModel
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
	if data.State.IsNull() {
		data.State = types.StringValue("active")
	}
	centers, err := d.client.CostCenters(ctx, enterprise.ValueString(), data.State.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("List cost centers", err.Error())
		return
	}
	values := make([]attr.Value, 0, len(centers))
	for _, summary := range centers {
		// List responses may carry summarized resources: hydrate every center in full.
		cc, err := d.client.CostCenter(ctx, enterprise.ValueString(), summary.ID)
		if err != nil {
			resp.Diagnostics.AddError("Read cost center "+summary.ID, err.Error())
			return
		}
		obj, ds := costCenterObject(*cc)
		resp.Diagnostics.Append(ds...)
		values = append(values, obj)
	}
	var ds diag.Diagnostics
	data.CostCenters, ds = types.ListValue(types.ObjectType{AttrTypes: costCenterTypes}, values)
	resp.Diagnostics.Append(ds...)
	data.ID = types.StringValue(enterprise.ValueString() + "/" + data.State.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
