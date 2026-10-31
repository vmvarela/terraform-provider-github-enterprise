# Caps what one organization can spend on Actions. GitHub enforces the limit;
# destroying this resource removes the cap.
resource "github_enterprise_budget" "actions" {
  provider = github-enterprise

  budget_scope          = "organization"
  budget_entity_name    = "my-org"
  budget_type           = "ProductPricing"
  budget_product_sku    = "actions"
  budget_amount         = 500
  prevent_further_usage = true

  alerts_enabled   = true
  alert_recipients = ["platform-owner"]
}

# AI credits are priced per bundle, so they use BundlePricing and a user scope
# requires the login and a blocking budget.
resource "github_enterprise_budget" "ai_credits" {
  provider = github-enterprise

  budget_scope          = "user"
  username              = "octocat"
  budget_type           = "BundlePricing"
  budget_product_sku    = "ai_credits"
  budget_amount         = 30
  prevent_further_usage = true
  expires_at            = "2027-01-01"
}
