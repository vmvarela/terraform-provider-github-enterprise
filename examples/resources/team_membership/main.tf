resource "githubenterprise_team_membership" "example" {
  enterprise_slug = "my-enterprise"
  team_id         = githubenterprise_team.example.team_id
  username        = "octocat"
}
