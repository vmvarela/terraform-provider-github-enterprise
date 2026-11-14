package provider

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// Live secret scanning tests create a real pattern, so they require an explicit
// confirmation of the enterprise in the same command:
//
//	GITHUB_ENTERPRISE_TEST_SECRET_SCANNING_CONFIRM=<enterprise slug>
//
// Nothing pre-existing is adopted, edited or deleted.
func secretScanningGate(t *testing.T) string {
	t.Helper()
	enterprise := liveGate(t)
	if os.Getenv("GITHUB_ENTERPRISE_TEST_SECRET_SCANNING_CONFIRM") != enterprise {
		t.Fatalf("GITHUB_ENTERPRISE_TEST_SECRET_SCANNING_CONFIRM must be set to exactly %q in the same command to confirm the intended enterprise; got %q", enterprise, os.Getenv("GITHUB_ENTERPRISE_TEST_SECRET_SCANNING_CONFIRM"))
	}
	return enterprise
}

func TestAccSecretScanningCustomPattern(t *testing.T) {
	enterprise := secretScanningGate(t)
	base := liveBaseURL(t)
	c := liveClient(t)
	var patternID int64
	name := "tf-acc-" + acctest.RandStringFromCharSet(10, acctest.CharSetAlphaNum)
	cfg := func(pattern string) string {
		return fmt.Sprintf(`provider "github-enterprise" {
  base_url = %q
}

resource "github_enterprise_secret_scanning_custom_pattern" "test" {
  provider = github-enterprise

  enterprise_slug = %q
  name            = %q
  pattern         = %q
}`, base, enterprise, name, pattern)
	}
	// The two lists are not configured, and GitHub may report an unset list as
	// absent or as empty; an import cannot tell the two apart.
	captureID := func(state *terraform.State) error {
		p, err := primaryState(state, spAddr)
		if err != nil {
			return err
		}
		id, err := strconv.ParseInt(p.Attributes["pattern_id"], 10, 64)
		if err != nil || id <= 0 {
			return fmt.Errorf("invalid pattern_id %q in state", p.Attributes["pattern_id"])
		}
		patternID = id
		return nil
	}
	checkIDStable := func(state *terraform.State) error {
		p, err := primaryState(state, spAddr)
		if err != nil {
			return err
		}
		if got, _ := strconv.ParseInt(p.Attributes["pattern_id"], 10, 64); got != patternID {
			return fmt.Errorf("pattern_id changed from %d to %s across a refresh or update", patternID, p.Attributes["pattern_id"])
		}
		return nil
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: factories, CheckDestroy: func(_ *terraform.State) error {
		p, err := c.GetCustomPattern(context.Background(), enterprise, patternID)
		if err != nil {
			return fmt.Errorf("verify removal of pattern %d: %w", patternID, err)
		}
		if p != nil {
			return fmt.Errorf("pattern %d still exists after destroy; inspect the enterprise and resolve it manually", patternID)
		}
		return nil
	}, Steps: []resource.TestStep{
		{Config: cfg("tf-acc-[0-9]{4}"), Check: resource.ComposeTestCheckFunc(
			captureID,
			resource.TestCheckResourceAttr(spAddr, "state", "unpublished"),
			resource.TestCheckResourceAttr(spAddr, "post_delete_action", "resolve_alerts"),
		)},
		// Read back without changes: refresh must not rewrite or replace the pattern.
		{Config: cfg("tf-acc-[0-9]{4}"), Check: checkIDStable},
		{Config: cfg("tf-acc-[0-9]{6}"), Check: checkIDStable},
		{ResourceName: spAddr, ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"must_match", "must_not_match"}},
	}})
}
