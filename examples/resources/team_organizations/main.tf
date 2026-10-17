resource "github_enterprise_team_organizations" "example" {
  provider = github-enterprise

  enterprise_slug    = "my-enterprise"
  team_id            = github_enterprise_team.example.team_id
  organization_slugs = ["my-organization"]
}
