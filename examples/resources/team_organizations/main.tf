resource "githubenterprise_team_organizations" "example" {
  enterprise_slug    = "my-enterprise"
  team_id            = githubenterprise_team.example.team_id
  organization_slugs = ["my-organization"]
}
