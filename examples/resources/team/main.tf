resource "github_enterprise_team" "example" {
  provider = github-enterprise

  enterprise_slug             = "my-enterprise"
  name                        = "Platform"
  description                 = "Platform Engineering"
  organization_selection_type = "selected"
}
