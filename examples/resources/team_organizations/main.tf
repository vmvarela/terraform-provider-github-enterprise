resource "github_enterprise_team_organizations" "example" {
  provider = github-enterprise

  team_id            = github_enterprise_team.example.team_id
  organization_slugs = ["my-organization"]
}
