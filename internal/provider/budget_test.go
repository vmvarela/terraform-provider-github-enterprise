package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/vmvarela/terraform-provider-github-enterprise/internal/client"
)

func budgetModelFor(scope, entity, user, sku, typ string, prevent bool) *budgetModel {
	m := &budgetModel{
		Enterprise:          types.StringValue("ent"),
		Scope:               types.StringValue(scope),
		EntityName:          types.StringNull(),
		User:                types.StringNull(),
		Amount:              types.Int64Value(100),
		PreventFurtherUsage: types.BoolValue(prevent),
		Type:                types.StringValue(typ),
		SKU:                 types.StringNull(),
		AlertsEnabled:       types.BoolValue(false),
		AlertRecipients:     types.ListNull(types.StringType),
		ExpiresAt:           types.StringNull(),
	}
	if entity != "" {
		m.EntityName = types.StringValue(entity)
	}
	if user != "" {
		m.User = types.StringValue(user)
	}
	if sku != "" {
		m.SKU = types.StringValue(sku)
	}
	return m
}

func TestBudgetValidation(t *testing.T) {
	valid := []*budgetModel{
		budgetModelFor("enterprise", "", "", "actions", "ProductPricing", false),
		budgetModelFor("organization", "acme", "", "actions", "ProductPricing", true),
		budgetModelFor("repository", "acme/repo", "", "actions_linux", "SkuPricing", false),
		budgetModelFor("cost_center", "uuid", "", "packages", "ProductPricing", false),
		budgetModelFor("multi_user_customer", "", "", "ai_credits", "BundlePricing", true),
		budgetModelFor("multi_user_cost_center", "uuid", "", "premium_requests", "SkuPricing", true),
		budgetModelFor("user", "", "octocat", "ai_credits", "BundlePricing", true),
	}
	for _, m := range valid {
		if d := validateBudget(m); d.HasError() {
			t.Errorf("%s/%s rejected: %v", m.Scope.ValueString(), m.SKU.ValueString(), d)
		}
	}

	invalid := map[string]*budgetModel{
		"organization without entity":   budgetModelFor("organization", "", "", "actions", "ProductPricing", false),
		"enterprise with entity":        budgetModelFor("enterprise", "acme", "", "actions", "ProductPricing", false),
		"user without login":            budgetModelFor("user", "", "", "ai_credits", "BundlePricing", true),
		"non-user with login":           budgetModelFor("enterprise", "", "octocat", "actions", "ProductPricing", false),
		"user scope with wrong sku":     budgetModelFor("user", "", "octocat", "actions", "BundlePricing", true),
		"user scope without blocking":   budgetModelFor("user", "", "octocat", "ai_credits", "BundlePricing", false),
		"bundle with non-ai sku":        budgetModelFor("enterprise", "", "", "actions", "BundlePricing", false),
		"cost center without entity":    budgetModelFor("cost_center", "", "", "packages", "ProductPricing", false),
		"multi_user without blocking":   budgetModelFor("multi_user_customer", "", "", "ai_credits", "BundlePricing", false),
		"repository without owner/repo": budgetModelFor("repository", "", "", "actions", "SkuPricing", false),
	}
	for name, m := range invalid {
		if d := validateBudget(m); !d.HasError() {
			t.Errorf("%s accepted", name)
		}
	}

	withExpiry := budgetModelFor("enterprise", "", "", "actions", "ProductPricing", false)
	withExpiry.ExpiresAt = types.StringValue("2027-01-01")
	if d := validateBudget(withExpiry); !d.HasError() {
		t.Error("expires_at outside user scope accepted")
	}
}

// GitHub answers with the entity's display name; a configured login must survive
// a refresh unchanged or every plan would show a diff.
func TestBudgetRefreshKeepsConfiguredEntityName(t *testing.T) {
	m := budgetModelFor("organization", "acme", "", "actions", "ProductPricing", false)
	api := &client.Budget{ID: "b1", Scope: "organization", EntityName: "Acme Inc", Amount: 100, Type: "ProductPricing", SKU: "actions"}
	if d := m.refresh(context.Background(), api); d.HasError() {
		t.Fatal(d)
	}
	if m.EntityName.ValueString() != "acme" {
		t.Errorf("configured entity name overwritten: %q", m.EntityName.ValueString())
	}
	if m.ID.ValueString() != "ent:b1" || m.BudgetID.ValueString() != "b1" {
		t.Errorf("identity %q %q", m.ID.ValueString(), m.BudgetID.ValueString())
	}

	imported := budgetModelFor("organization", "", "", "actions", "ProductPricing", false)
	if d := imported.refresh(context.Background(), api); d.HasError() {
		t.Fatal(d)
	}
	if imported.EntityName.ValueString() != "Acme Inc" {
		t.Errorf("import should adopt the API value, got %q", imported.EntityName.ValueString())
	}
}

func TestBudgetImportID(t *testing.T) {
	for _, bad := range []string{"", "ent", "ent:", ":b1", "Ent:b1", "ent:b1:x", "ent/a:b", "ent:..", "ent: "} {
		if _, _, err := parseBudgetImportID(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	ent, id, err := parseBudgetImportID("ent:0b3f2b1a-1111-2222-3333-444455556666")
	if err != nil || ent != "ent" || id != "0b3f2b1a-1111-2222-3333-444455556666" {
		t.Fatalf("%q %q %v", ent, id, err)
	}
}

// A budget cannot move between entities, and GitHub echoes the entity's display
// name instead of the configured login. Requiring replacement would therefore
// destroy and recreate a real spending cap the first time an imported budget is
// applied, so the attribute must stay in place.
func TestBudgetEntityNameDoesNotForceReplacement(t *testing.T) {
	r := &budgetResource{}
	var sr resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &sr)
	attr := sr.Schema.Attributes["budget_entity_name"].(schema.StringAttribute)
	if len(attr.PlanModifiers) != 0 {
		t.Fatalf("budget_entity_name must not require replacement, got %d plan modifiers", len(attr.PlanModifiers))
	}
}
