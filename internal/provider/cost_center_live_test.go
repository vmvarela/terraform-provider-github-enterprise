package provider

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/vmvarela/terraform-provider-github-enterprise/internal/client"
)

// Live cost center acceptance tests reuse liveGate/liveBaseURL (see their
// safety model). Additionally:
//   - Only a cost center whose opaque ID is absent from a pre-test listing of
//     active AND deleted centers is accepted; it is archived on destroy and the
//     exact ID is verified. Nothing is adopted, swept or looked up by name.
//   - The suites use guardedFactories: teardown refuses to archive any baseline
//     or empty ID, checked against the actual state being destroyed.
//   - Cost centers cannot be hard-deleted: each run leaves one archived center.
//   - Assignment tests need explicit disposable fixtures (see CONTRIBUTING).
//     The provider's own preflight refuses any fixture already held by another
//     cost center, so external baseline membership is never moved.

func costCenterBaseline(t *testing.T, c *client.Client, enterprise string) map[string]bool {
	t.Helper()
	ids := map[string]bool{}
	for _, state := range []string{"active", "deleted"} {
		list, err := c.CostCenters(context.Background(), enterprise, state)
		if err != nil {
			t.Fatalf("list %s cost centers: %v", state, err)
		}
		for _, cc := range list {
			ids[cc.ID] = true
		}
	}
	return ids
}

// captureCostCenter records the exact ID of the center created by this test.
func captureCostCenter(created *string, baseline map[string]bool) resource.TestCheckFunc {
	return func(state *terraform.State) error {
		p, err := primaryState(state, "github_enterprise_cost_center.test")
		if err != nil {
			return err
		}
		id := p.Attributes["cost_center_id"]
		if id == "" || baseline[id] {
			return fmt.Errorf("cost center %q is empty or existed before the test; refusing to manage it; the guarded test provider refuses to archive it on teardown", id)
		}
		*created = id
		return nil
	}
}

func checkCostCenterIDStable(created *string) resource.TestCheckFunc {
	return func(state *terraform.State) error {
		p, err := primaryState(state, "github_enterprise_cost_center.test")
		if err != nil {
			return err
		}
		if p.Attributes["cost_center_id"] != *created {
			return fmt.Errorf("cost center ID changed from %q to %q", *created, p.Attributes["cost_center_id"])
		}
		return nil
	}
}

// checkCostCenterArchived verifies the exact test-created center is archived
// and holds no assignments. It never changes anything.
func checkCostCenterArchived(c *client.Client, enterprise string, created *string) func(*terraform.State) error {
	return func(*terraform.State) error {
		if *created == "" {
			return fmt.Errorf("no test-created cost center ID to verify")
		}
		cc, err := c.CostCenter(context.Background(), enterprise, *created)
		if err != nil {
			return fmt.Errorf("verify archive of cost center %s: %w", *created, err)
		}
		if !!strings.EqualFold(cc.GetState(), "deleted") || len(cc.Resources) != 0 {
			return fmt.Errorf("test cost center %s must be archived and empty (state %q, %d resources); inspect and resolve it manually", *created, cc.GetState(), len(cc.Resources))
		}
		return nil
	}
}

func checkListedCostCenter(dataSource string, created *string) resource.TestCheckFunc {
	return func(state *terraform.State) error {
		p, err := primaryState(state, dataSource)
		if err != nil {
			return err
		}
		for k, v := range p.Attributes {
			if strings.HasPrefix(k, "cost_centers.") && strings.HasSuffix(k, ".cost_center_id") && v == *created {
				return nil
			}
		}
		return fmt.Errorf("%s does not list test cost center %q", dataSource, *created)
	}
}

func TestAccCostCenter(t *testing.T) {
	enterprise := liveGate(t)
	c := liveClient(t)
	baseline := costCenterBaseline(t, c, enterprise)
	created := ""
	name := "tf-acc-" + acctest.RandStringFromCharSet(10, acctest.CharSetAlphaNum)
	cfg := func(n string) string {
		return fmt.Sprintf(`provider "github-enterprise" {}
resource "github_enterprise_cost_center" "test" {
  provider        = github-enterprise
  enterprise_slug = %q
  name            = %q
}
data "github_enterprise_cost_center" "test" {
  provider        = github-enterprise
  enterprise_slug = %q
  cost_center_id  = github_enterprise_cost_center.test.cost_center_id
}
data "github_enterprise_cost_centers" "test" {
  provider        = github-enterprise
  enterprise_slug = %q
  depends_on      = [github_enterprise_cost_center.test]
}`, enterprise, n, enterprise, enterprise)
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: guardedFactories(baseline), CheckDestroy: checkCostCenterArchived(c, enterprise, &created), Steps: []resource.TestStep{
		{Config: cfg(name), Check: resource.ComposeTestCheckFunc(
			captureCostCenter(&created, baseline),
			resource.TestCheckResourceAttr("data.github_enterprise_cost_center.test", "name", name),
			checkListedCostCenter("data.github_enterprise_cost_centers.test", &created),
		)},
		{Config: cfg(name + "-renamed"), Check: resource.ComposeTestCheckFunc(
			checkCostCenterIDStable(&created),
			resource.TestCheckResourceAttr("github_enterprise_cost_center.test", "name", name+"-renamed"),
		)},
		{ResourceName: "github_enterprise_cost_center.test", ImportState: true, ImportStateVerify: true},
	}})
}

// liveFixture reads an explicit disposable fixture: VALUE plus a *_CONFIRM
// variable that must repeat it exactly. Unset means not requested.
func liveFixture(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		return ""
	}
	if os.Getenv(key+"_CONFIRM") != v {
		t.Fatalf("%s_CONFIRM must equal %s exactly to confirm a disposable fixture", key, key)
	}
	return v
}

func TestAccCostCenterResources(t *testing.T) {
	enterprise := liveGate(t)
	sets := map[string]string{}
	for key, attr := range map[string]string{"GITHUB_ENTERPRISE_TEST_CC_USER": "users", "GITHUB_ENTERPRISE_TEST_CC_ORG": "organizations", "GITHUB_ENTERPRISE_TEST_CC_REPO": "repositories", "GITHUB_ENTERPRISE_TEST_CC_TEAM": "enterprise_teams"} {
		if v := liveFixture(t, key); v != "" {
			sets[attr] = fmt.Sprintf("[%q]", v)
		}
	}
	if len(sets) == 0 {
		t.Skip("Set at least one GITHUB_ENTERPRISE_TEST_CC_{USER,ORG,REPO,TEAM} with its _CONFIRM to run the assignment suite")
	}
	c := liveClient(t)
	baseline := costCenterBaseline(t, c, enterprise)
	created := ""
	name := "tf-acc-" + acctest.RandStringFromCharSet(10, acctest.CharSetAlphaNum)
	// assign: nil = cost center only; otherwise include the assignment resource.
	cfg := func(assign *bool) string {
		var b strings.Builder
		fmt.Fprintf(&b, `provider "github-enterprise" {}
resource "github_enterprise_cost_center" "test" {
  provider        = github-enterprise
  enterprise_slug = %q
  name            = %q
}
`, enterprise, name)
		if assign == nil {
			return b.String()
		}
		fmt.Fprintf(&b, `resource "github_enterprise_cost_center_resources" "test" {
  provider        = github-enterprise
  enterprise_slug = %q
  cost_center_id  = github_enterprise_cost_center.test.cost_center_id
`, enterprise)
		if *assign {
			for k, v := range sets {
				fmt.Fprintf(&b, "  %s = %s\n", k, v)
			}
		}
		b.WriteString("}\n")
		return b.String()
	}
	yes, no := true, false
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: guardedFactories(baseline), CheckDestroy: checkCostCenterArchived(c, enterprise, &created), Steps: []resource.TestStep{
		// Create the cost center alone and verify it is new before any fixture is assigned.
		{Config: cfg(nil), Check: captureCostCenter(&created, baseline)},
		{Config: cfg(&yes), Check: resource.ComposeTestCheckFunc(checkCostCenterIDStable(&created), func(*terraform.State) error {
			cc, err := c.CostCenter(context.Background(), enterprise, created)
			if err != nil || len(cc.Resources) != len(sets) {
				return fmt.Errorf("test cost center %s should hold exactly the %d fixtures: %v %v", created, len(sets), cc, err)
			}
			return nil
		})},
		{ResourceName: "github_enterprise_cost_center_resources.test", ImportState: true, ImportStateVerify: true},
		{Config: cfg(&no), Check: resource.TestCheckResourceAttr("github_enterprise_cost_center_resources.test", "users.#", "0")},
	}})
}
