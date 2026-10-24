resource "github_enterprise_team" "example" {
  provider = github-enterprise

  name                        = "Platform"
  description                 = "Platform Engineering"
  organization_selection_type = "selected"
}
