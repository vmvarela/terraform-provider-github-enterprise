package provider

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/vmvarela/terraform-provider-github-enterprise/internal/client"
)

// Live budget tests create a real cap, so they require the organization fixture
// to be named explicitly in the same command:
//
//	GITHUB_ENTERPRISE_TEST_BUDGET_CONFIRM=<org login>
//
// The budget is created with a harmless amount and prevent_further_usage=false,
// then deleted. Nothing pre-existing is adopted, edited or deleted.
func budgetGate(t *testing.T) string {
	t.Helper()
	org := os.Getenv("GITHUB_ENTERPRISE_TEST_ORG_A")
	if org == "" {
		t.Fatal("GITHUB_ENTERPRISE_TEST_ORG_A is required")
	}
	if os.Getenv("GITHUB_ENTERPRISE_TEST_BUDGET_CONFIRM") != org {
		t.Fatalf("GITHUB_ENTERPRISE_TEST_BUDGET_CONFIRM must be set to exactly %q in the same command", org)
	}
	return org
}

func TestAccBudget(t *testing.T) {
	enterprise := liveGate(t)
	base := liveBaseURL(t)
	org := budgetGate(t)

	var budgetID string
	config := func(amount int) string {
		return fmt.Sprintf(`
provider "github-enterprise" {
  base_url = %q
}

resource "github_enterprise_budget" "test" {
  provider = github-enterprise

  enterprise_slug       = %q
  budget_scope          = "organization"
  budget_entity_name    = %q
  budget_type           = "ProductPricing"
  budget_product_sku    = "actions"
  budget_amount         = %d
  prevent_further_usage = false
}
`, base, enterprise, org, amount)
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		CheckDestroy: func(_ *terraform.State) error {
			c, err := client.New(base, os.Getenv("GITHUB_ENTERPRISE_TOKEN"), client.DefaultAPIVersion)
			if err != nil {
				return err
			}
			_, err = c.Budget(context.Background(), enterprise, budgetID)
			if !client.IsNotFound(err) {
				return fmt.Errorf("budget %q still readable after destroy: %v", budgetID, err)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: config(999999),
				Check: resource.ComposeAggregateTestCheckFunc(
					func(state *terraform.State) error {
						p, err := primaryState(state, "github_enterprise_budget.test")
						if err != nil {
							return err
						}
						budgetID = p.Attributes["budget_id"]
						if budgetID == "" {
							return fmt.Errorf("budget_id is empty")
						}
						return nil
					},
					resource.TestCheckResourceAttr("github_enterprise_budget.test", "budget_amount", "999999"),
					resource.TestCheckResourceAttr("github_enterprise_budget.test", "budget_entity_name", org),
				),
			},
			{
				Config: config(888888),
				Check:  resource.TestCheckResourceAttr("github_enterprise_budget.test", "budget_amount", "888888"),
			},
			{
				ResourceName:            "github_enterprise_budget.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"budget_entity_name"},
			},
		},
	})
}
