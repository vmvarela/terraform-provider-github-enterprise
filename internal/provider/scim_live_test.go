package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccSCIM is opt-in and read-only. It reads two existing approved fixtures
// by exact ID and their lists with exact `id eq` filters. It never provisions,
// links, changes membership or tears anything down; never run it against
// production identities.
func TestAccSCIM(t *testing.T) {
	enterprise := liveGate(t)
	base := liveBaseURL(t)
	groupID := liveFixture(t, "GITHUB_ENTERPRISE_TEST_SCIM_GROUP_ID")
	userID := liveFixture(t, "GITHUB_ENTERPRISE_TEST_SCIM_USER_ID")
	if groupID == "" || userID == "" {
		t.Skip("Set GITHUB_ENTERPRISE_TEST_SCIM_GROUP_ID and GITHUB_ENTERPRISE_TEST_SCIM_USER_ID, each with its _CONFIRM, to run the read-only SCIM suite")
	}
	cfg := fmt.Sprintf(`
provider "github-enterprise" {
  base_url = %q
}
data "github_enterprise_scim_group" "test" {
  provider        = github-enterprise
  enterprise_slug = %q
  scim_group_id   = %q
}
data "github_enterprise_scim_groups" "test" {
  provider        = github-enterprise
  enterprise_slug = %q
  filter          = %q
}
data "github_enterprise_scim_user" "test" {
  provider        = github-enterprise
  enterprise_slug = %q
  scim_user_id    = %q
}
data "github_enterprise_scim_users" "test" {
  provider        = github-enterprise
  enterprise_slug = %q
  filter          = %q
}
`, base, enterprise, groupID, enterprise, fmt.Sprintf("id eq %q", groupID), enterprise, userID, enterprise, fmt.Sprintf("id eq %q", userID))
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories, Steps: []resource.TestStep{
		{Config: cfg, Check: resource.ComposeTestCheckFunc(
			resource.TestCheckResourceAttr("data.github_enterprise_scim_group.test", "id", enterprise+"/"+groupID),
			resource.TestCheckResourceAttr("data.github_enterprise_scim_group.test", "scim_group_id", groupID),
			resource.TestCheckResourceAttr("data.github_enterprise_scim_groups.test", "groups.#", "1"),
			resource.TestCheckResourceAttr("data.github_enterprise_scim_groups.test", "groups.0.scim_group_id", groupID),
			resource.TestCheckResourceAttr("data.github_enterprise_scim_user.test", "id", enterprise+"/"+userID),
			resource.TestCheckResourceAttr("data.github_enterprise_scim_user.test", "scim_user_id", userID),
			resource.TestCheckResourceAttr("data.github_enterprise_scim_users.test", "users.#", "1"),
			resource.TestCheckResourceAttr("data.github_enterprise_scim_users.test", "users.0.scim_user_id", userID),
		)},
	}})
}
