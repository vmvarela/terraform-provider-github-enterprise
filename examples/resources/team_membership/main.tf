resource "github_enterprise_team_membership" "example" {
  provider = github-enterprise

  team_id  = github_enterprise_team.example.team_id
  username = "octocat"
}
