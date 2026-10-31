package provider

import (
	"context"
	"net/url"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/vmvarela/terraform-provider-github-enterprise/internal/client"
)

var budgetTypes_ = map[string]attr.Type{
	"budget_id":             types.StringType,
	"budget_scope":          types.StringType,
	"budget_entity_name":    types.StringType,
	"user":                  types.StringType,
	"budget_amount":         types.Int64Type,
	"prevent_further_usage": types.BoolType,
	"budget_type":           types.StringType,
	"budget_product_sku":    types.StringType,
	"alerts_enabled":        types.BoolType,
	"alert_recipients":      types.ListType{ElemType: types.StringType},
	"expires_at":            types.StringType,
	"consumed_amount":       types.Float64Type,
}

func budgetDataAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"budget_scope":          schema.StringAttribute{Computed: true, MarkdownDescription: "Scope the budget applies to."},
		"budget_entity_name":    schema.StringAttribute{Computed: true, MarkdownDescription: "Entity the budget applies to, as GitHub reports it. GitHub answers with the entity's display name, so this may differ from the login configured on the resource."},
		"user":                  schema.StringAttribute{Computed: true, MarkdownDescription: "Login the budget applies to, or null for other scopes."},
		"budget_amount":         schema.Int64Attribute{Computed: true, MarkdownDescription: "Budget amount in whole dollars, or a number of licenses for license-based products."},
		"prevent_further_usage": schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether GitHub blocks additional spending once the budget is exceeded."},
		"budget_type":           schema.StringAttribute{Computed: true, MarkdownDescription: "Pricing model: `ProductPricing`, `SkuPricing` or `BundlePricing`."},
		"budget_product_sku":    schema.StringAttribute{Computed: true, MarkdownDescription: "Product or SKU covered by the budget, or null."},
		"alerts_enabled":        schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether GitHub sends budget alerts."},
		"alert_recipients":      schema.ListAttribute{Computed: true, ElementType: types.StringType, MarkdownDescription: "Logins that receive budget alerts. Always an empty list when alerts are disabled."},
		"expires_at":            schema.StringAttribute{Computed: true, MarkdownDescription: "Expiry date in YYYY-MM-DD format, or null."},
		"consumed_amount":       schema.Float64Attribute{Computed: true, MarkdownDescription: "Amount consumed so far, in the budget's unit. GitHub rounds it to two decimals when one budget is read and returns more precision in the list, so the two data sources can disagree on the fraction."},
	}
}

func budgetObject(b *client.Budget) (types.Object, diag.Diagnostics) {
	recipients := b.AlertRecipients
	if recipients == nil {
		recipients = []string{}
	}
	list, ds := types.ListValueFrom(context.Background(), types.StringType, recipients)
	var all diag.Diagnostics
	all.Append(ds...)
	obj, ds := types.ObjectValue(budgetTypes_, map[string]attr.Value{
		"budget_id":             types.StringValue(b.ID),
		"budget_scope":          types.StringValue(b.Scope),
		"budget_entity_name":    nullIfEmpty(b.EntityName),
		"user":                  nullIfEmpty(b.User),
		"budget_amount":         types.Int64Value(b.Amount),
		"prevent_further_usage": types.BoolValue(b.PreventFurtherUsage),
		"budget_type":           types.StringValue(b.Type),
		"budget_product_sku":    nullIfEmpty(b.SKU),
		"alerts_enabled":        types.BoolValue(b.WillAlert),
		"alert_recipients":      list,
		"expires_at":            nullIfEmpty(b.ExpiresAt),
		"consumed_amount":       types.Float64Value(b.ConsumedAmount),
	})
	all.Append(ds...)
	return obj, all
}

type budgetDataSource struct{ configuredDataSource }
type budgetDataModel struct {
	ID                  types.String  `tfsdk:"id"`
	Enterprise          types.String  `tfsdk:"enterprise_slug"`
	BudgetID            types.String  `tfsdk:"budget_id"`
	Scope               types.String  `tfsdk:"budget_scope"`
	EntityName          types.String  `tfsdk:"budget_entity_name"`
	User                types.String  `tfsdk:"user"`
	Amount              types.Int64   `tfsdk:"budget_amount"`
	PreventFurtherUsage types.Bool    `tfsdk:"prevent_further_usage"`
	Type                types.String  `tfsdk:"budget_type"`
	SKU                 types.String  `tfsdk:"budget_product_sku"`
	AlertsEnabled       types.Bool    `tfsdk:"alerts_enabled"`
	AlertRecipients     types.List    `tfsdk:"alert_recipients"`
	ExpiresAt           types.String  `tfsdk:"expires_at"`
	ConsumedAmount      types.Float64 `tfsdk:"consumed_amount"`
}

func (d *budgetDataSource) Metadata(_ context.Context, _ datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = resourceTypePrefix + "_budget"
}
func (d *budgetDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	a := budgetDataAttributes()
	a["id"] = schema.StringAttribute{Computed: true, MarkdownDescription: "enterprise_slug:budget_id."}
	a["enterprise_slug"] = enterpriseDataAttribute()
	a["budget_id"] = schema.StringAttribute{Required: true, MarkdownDescription: "Opaque budget ID assigned by GitHub.", Validators: []validator.String{costCenterIDRule()}}
	resp.Schema = schema.Schema{MarkdownDescription: "Reads one billing budget, including the amount consumed so far. Read-only: it never creates or changes a budget.", Attributes: a}
}
func (d *budgetDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data budgetDataModel
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
	b, err := d.client.Budget(ctx, enterprise.ValueString(), data.BudgetID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Read budget", err.Error())
		return
	}
	obj, ds := budgetObject(b)
	resp.Diagnostics.Append(ds...)
	if resp.Diagnostics.HasError() {
		return
	}
	a := obj.Attributes()
	data.ID = types.StringValue(joinID(enterprise.ValueString(), b.ID))
	data.Scope, data.EntityName, data.User = a["budget_scope"].(types.String), a["budget_entity_name"].(types.String), a["user"].(types.String)
	data.Amount, data.PreventFurtherUsage = a["budget_amount"].(types.Int64), a["prevent_further_usage"].(types.Bool)
	data.Type, data.SKU, data.ExpiresAt = a["budget_type"].(types.String), a["budget_product_sku"].(types.String), a["expires_at"].(types.String)
	data.AlertsEnabled, data.AlertRecipients = a["alerts_enabled"].(types.Bool), a["alert_recipients"].(types.List)
	data.ConsumedAmount = a["consumed_amount"].(types.Float64)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

type budgetsDataSource struct{ configuredDataSource }
type budgetsDataModel struct {
	ID         types.String `tfsdk:"id"`
	Enterprise types.String `tfsdk:"enterprise_slug"`
	Scope      types.String `tfsdk:"scope"`
	User       types.String `tfsdk:"user"`
	Budgets    types.List   `tfsdk:"budgets"`
}

func (d *budgetsDataSource) Metadata(_ context.Context, _ datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = resourceTypePrefix + "_budgets"
}
func (d *budgetsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	nested := budgetDataAttributes()
	nested["budget_id"] = schema.StringAttribute{Computed: true, MarkdownDescription: "Opaque budget ID assigned by GitHub."}
	resp.Schema = schema.Schema{MarkdownDescription: "Lists billing budgets with the amount consumed so far, following every page. Read-only.", Attributes: map[string]schema.Attribute{
		"id":              schema.StringAttribute{Computed: true, MarkdownDescription: "Deterministic query identity: enterprise_slug:budgets, plus the escaped filters when set."},
		"enterprise_slug": enterpriseDataAttribute(),
		"scope":           schema.StringAttribute{Optional: true, MarkdownDescription: "Only return budgets with this scope.", Validators: []validator.String{oneOf(budgetScopes...)}},
		"user":            schema.StringAttribute{Optional: true, MarkdownDescription: "Only return budgets for this login. Use it together with scope `user`.", Validators: []validator.String{usernameRule()}},
		"budgets":         schema.ListNestedAttribute{Computed: true, MarkdownDescription: "Budgets in API order.", NestedObject: schema.NestedAttributeObject{Attributes: nested}},
	}}
}
func (d *budgetsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data budgetsDataModel
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
	scope, user := "", ""
	if !data.Scope.IsNull() {
		scope = data.Scope.ValueString()
	}
	if !data.User.IsNull() {
		user = data.User.ValueString()
	}
	budgets, err := d.client.Budgets(ctx, enterprise.ValueString(), scope, user)
	if err != nil {
		resp.Diagnostics.AddError("List budgets", err.Error())
		return
	}
	values := make([]attr.Value, 0, len(budgets))
	for _, b := range budgets {
		obj, ds := budgetObject(b)
		resp.Diagnostics.Append(ds...)
		values = append(values, obj)
	}
	var ds diag.Diagnostics
	data.Budgets, ds = types.ListValue(types.ObjectType{AttrTypes: budgetTypes_}, values)
	resp.Diagnostics.Append(ds...)
	id := joinID(enterprise.ValueString(), "budgets")
	if scope != "" {
		id += "?scope=" + url.QueryEscape(scope)
	}
	if user != "" {
		id += "?user=" + url.QueryEscape(user)
	}
	data.ID = types.StringValue(id)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
