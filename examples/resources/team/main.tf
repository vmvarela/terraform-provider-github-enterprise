resource "githubenterprise_team" "example" {
  enterprise_slug             = "my-enterprise"
  name                        = "Platform"
  description                 = "Platform Engineering"
  organization_selection_type = "selected"
}
