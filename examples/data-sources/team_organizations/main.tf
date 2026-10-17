data "github_enterprise_team_organizations" "example" {
  provider = github-enterprise

  enterprise_slug = "my-enterprise"
  team_id         = 123456
}
