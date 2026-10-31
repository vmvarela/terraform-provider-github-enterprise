package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/vmvarela/terraform-provider-github-enterprise/internal/client"
)

var _ resource.ResourceWithImportState = &budgetResource{}

// Budget scopes and pricing types accepted by the billing budgets API. The API
// reference omits BundlePricing from its enum, but it is required for AI credit
// budgets and GitHub returns it.
var (
	budgetScopes = []string{"enterprise", "organization", "repository", "cost_center", "multi_user_customer", "multi_user_cost_center", "user"}
	budgetTypes  = []string{"ProductPricing", "SkuPricing", "BundlePricing"}
)

type budgetResource struct{ configuredResource }
type budgetModel struct {
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

func (m *budgetModel) refresh(ctx context.Context, b *client.Budget) diag.Diagnostics {
	m.ID = types.StringValue(joinID(m.Enterprise.ValueString(), b.ID))
	m.BudgetID = types.StringValue(b.ID)
	m.Scope = types.StringValue(b.Scope)
	m.User = nullIfEmpty(b.User)
	m.Amount = types.Int64Value(b.Amount)
	m.PreventFurtherUsage = types.BoolValue(b.PreventFurtherUsage)
	m.Type = types.StringValue(b.Type)
	m.SKU = nullIfEmpty(b.SKU)
	m.AlertsEnabled = types.BoolValue(b.WillAlert)
	m.ExpiresAt = nullIfEmpty(b.ExpiresAt)
	m.ConsumedAmount = floatOrNull(b.ConsumedAmount)
	// GitHub answers with the entity's display name, not the login or ID that
	// was configured, so a known value is never overwritten from the API.
	if m.EntityName.IsNull() || m.EntityName.ValueString() == "" {
		m.EntityName = nullIfEmpty(b.EntityName)
	}
	recipients, diags := types.ListValueFrom(ctx, types.StringType, recipientsOf(b.AlertRecipients))
	m.AlertRecipients = recipients
	return diags
}

func recipientsOf(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

func (m *budgetModel) input() client.BudgetInput {
	return client.BudgetInput{
		Scope:               m.Scope.ValueString(),
		EntityName:          m.EntityName.ValueString(),
		User:                m.User.ValueString(),
		Amount:              m.Amount.ValueInt64(),
		PreventFurtherUsage: m.PreventFurtherUsage.ValueBool(),
		Type:                m.Type.ValueString(),
		SKU:                 m.SKU.ValueString(),
		WillAlert:           m.AlertsEnabled.ValueBool(),
		AlertRecipients:     listOf(m.AlertRecipients),
		ExpiresAt:           m.ExpiresAt.ValueString(),
	}
}

func listOf(l types.List) []string {
	if l.IsNull() || l.IsUnknown() {
		return nil
	}
	out := make([]string, 0, len(l.Elements()))
	for _, e := range l.Elements() {
		out = append(out, e.(types.String).ValueString())
	}
	return out
}

// validateBudget rejects combinations the API refuses with an opaque 422.
func validateBudget(m *budgetModel) diag.Diagnostics {
	var diags diag.Diagnostics
	scope, sku := m.Scope.ValueString(), m.SKU.ValueString()
	entity, user := m.EntityName.ValueString(), m.User.ValueString()

	needsEntity := map[string]bool{"organization": true, "repository": true, "cost_center": true, "multi_user_cost_center": true}[scope]
	if needsEntity && entity == "" {
		diags.AddAttributeError(path.Root("budget_entity_name"), "Missing budget_entity_name", fmt.Sprintf("budget_scope %q requires budget_entity_name", scope))
	}
	if !needsEntity && entity != "" {
		diags.AddAttributeError(path.Root("budget_entity_name"), "Unused budget_entity_name", fmt.Sprintf("budget_scope %q does not use budget_entity_name", scope))
	}
	if scope == "user" && user == "" {
		diags.AddAttributeError(path.Root("user"), "Missing user", "budget_scope \"user\" requires user")
	}
	if scope != "user" && user != "" {
		diags.AddAttributeError(path.Root("user"), "Unused user", fmt.Sprintf("user is only used when budget_scope is \"user\", not %q", scope))
	}
	if scope != "user" && !m.ExpiresAt.IsNull() {
		diags.AddAttributeError(path.Root("expires_at"), "Unsupported expires_at", "expires_at is only supported for user-scoped budgets")
	}
	aiScopes := map[string]bool{"user": true, "multi_user_customer": true, "multi_user_cost_center": true}[scope]
	if aiScopes && sku != "ai_credits" && sku != "premium_requests" {
		diags.AddAttributeError(path.Root("budget_product_sku"), "Unsupported budget_product_sku", fmt.Sprintf("budget_scope %q only supports ai_credits or premium_requests", scope))
	}
	if aiScopes && !m.PreventFurtherUsage.ValueBool() {
		diags.AddAttributeError(path.Root("prevent_further_usage"), "Unsupported prevent_further_usage", fmt.Sprintf("budget_scope %q requires prevent_further_usage to be true", scope))
	}
	if m.Type.ValueString() == "BundlePricing" && sku != "ai_credits" {
		diags.AddAttributeError(path.Root("budget_product_sku"), "Unsupported budget_product_sku", "budget_type \"BundlePricing\" requires budget_product_sku \"ai_credits\"")
	}
	return diags
}

func (r *budgetResource) Metadata(_ context.Context, _ resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = resourceTypePrefix + "_budget"
}
func (r *budgetResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := func() []planmodifier.String { return []planmodifier.String{stringplanmodifier.RequiresReplace()} }
	resp.Schema = schema.Schema{MarkdownDescription: "Manages a GitHub Enterprise Cloud billing budget. A budget caps spending for one scope (enterprise, organization, repository, cost center, all users, all users of a cost center, or one user). GitHub enforces the cap server-side; this resource only declares it. **Destroying it removes the cap**; spending already incurred is unaffected. Amounts are whole dollars, or a number of licenses for license-based products. Requires an enterprise owner or billing manager token with `manage_billing:enterprise`; fine-grained tokens are not supported by these endpoints.", Attributes: map[string]schema.Attribute{
		"id":              schema.StringAttribute{Computed: true, MarkdownDescription: "Stable identity: enterprise_slug:budget_id.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"enterprise_slug": schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Enterprise account slug. Defaults to the provider-level enterprise_slug. Changing it replaces the resource.", Validators: []validator.String{enterpriseRule()}, PlanModifiers: replace()},
		"budget_id":       schema.StringAttribute{Computed: true, MarkdownDescription: "Opaque budget ID assigned by GitHub.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"budget_scope":    schema.StringAttribute{Required: true, MarkdownDescription: "Scope the budget applies to: `enterprise`, `organization`, `repository`, `cost_center`, `multi_user_customer`, `multi_user_cost_center` or `user`. Changing it replaces the budget.", Validators: []validator.String{oneOf(budgetScopes...)}, PlanModifiers: replace()},
		// Deliberately no RequiresReplace: GitHub cannot move a budget between
		// entities, so replacing on a config/state difference would destroy and
		// recreate a real spending cap after an import. Changing the value only
		// updates what Terraform records.
		"budget_entity_name":    schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Entity the budget applies to: organization login, `owner/repo`, or cost center UUID. Not used by `enterprise`, `multi_user_customer` and `user`. GitHub answers with the entity's display name, so a configured value is never overwritten and an imported budget records the display name until the next apply. Changing this value only updates what Terraform records; a budget cannot move between entities, so recreate it to target a different one."},
		"user":                  schema.StringAttribute{Optional: true, MarkdownDescription: "GitHub login the budget applies to. Required when `budget_scope` is `user`. Changing it replaces the budget.", Validators: []validator.String{usernameRule()}, PlanModifiers: replace()},
		"budget_amount":         schema.Int64Attribute{Required: true, MarkdownDescription: "Budget amount in whole dollars, or a number of licenses for license-based products. Fractions are not accepted by the API."},
		"prevent_further_usage": schema.BoolAttribute{Required: true, MarkdownDescription: "Whether GitHub blocks additional spending once the budget is exceeded. Must be true for `user`, `multi_user_customer` and `multi_user_cost_center` scopes."},
		"budget_type":           schema.StringAttribute{Required: true, MarkdownDescription: "Pricing model: `ProductPricing` (all SKUs of a product), `SkuPricing` (one SKU) or `BundlePricing` (AI credits). Changing it replaces the budget.", Validators: []validator.String{oneOf(budgetTypes...)}, PlanModifiers: replace()},
		"budget_product_sku":    schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Product or SKU covered by the budget, for example `actions`, `packages`, `codespaces`, `actions_linux` or `ai_credits`. Changing it replaces the budget.", PlanModifiers: replace()},
		"alerts_enabled":        schema.BoolAttribute{Optional: true, Computed: true, MarkdownDescription: "Whether GitHub sends budget alerts. Always disabled for `user` scope.", PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()}},
		"alert_recipients":      schema.ListAttribute{Optional: true, Computed: true, ElementType: types.StringType, MarkdownDescription: "Logins that receive budget alerts. Rejected for `user` scope.", PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()}},
		"expires_at":            schema.StringAttribute{Optional: true, MarkdownDescription: "Expiry date in YYYY-MM-DD format. Only supported for `user` scope. Changing it replaces the budget.", Validators: []validator.String{dateRule()}, PlanModifiers: replace()},
		"consumed_amount":       schema.Float64Attribute{Computed: true, MarkdownDescription: "Amount consumed so far, in the budget's unit. Read-only; it changes without any configuration change."},
	}}
}
func (r *budgetResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	applyEnterpriseSlugDefault(ctx, r.enterpriseSlug, req, resp)
}
func (r *budgetResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data budgetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(validateBudget(&data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	b, err := r.client.CreateBudget(ctx, data.Enterprise.ValueString(), data.input())
	if err != nil {
		resp.Diagnostics.AddError("Create budget", err.Error())
		return
	}
	resp.Diagnostics.Append(data.refresh(ctx, b)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
func (r *budgetResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data budgetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	b, err := r.client.Budget(ctx, data.Enterprise.ValueString(), data.BudgetID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Read budget", err.Error())
		return
	}
	resp.Diagnostics.Append(data.refresh(ctx, b)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
func (r *budgetResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data, old budgetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(validateBudget(&data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	data.Enterprise = old.Enterprise
	b, err := r.client.UpdateBudget(ctx, old.Enterprise.ValueString(), old.BudgetID.ValueString(), data.input())
	if err != nil {
		resp.Diagnostics.AddError("Update budget", err.Error())
		return
	}
	resp.Diagnostics.Append(data.refresh(ctx, b)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
func (r *budgetResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data budgetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client.DeleteBudget(ctx, data.Enterprise.ValueString(), data.BudgetID.ValueString())
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Delete budget", err.Error())
	}
}
func parseBudgetImportID(id string) (enterprise, budgetID string, err error) {
	parts := strings.Split(id, idSeparator)
	if len(parts) != 2 || !enterpriseSlugPattern.MatchString(parts[0]) || !validOpaqueID(parts[1]) {
		return "", "", fmt.Errorf("expected enterprise_slug:budget_id")
	}
	return parts[0], parts[1], nil
}

func (r *budgetResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	enterprise, budgetID, err := parseBudgetImportID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("enterprise_slug"), enterprise)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("budget_id"), budgetID)...)
}

func dateRule() validator.String {
	return stringRule{"Must be a date in YYYY-MM-DD format.", func(s string) bool {
		_, err := time.Parse("2006-01-02", s)
		return err == nil
	}}
}
