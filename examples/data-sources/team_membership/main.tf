data "github_enterprise_team_membership" "example" {
  provider = github-enterprise

  enterprise_slug = "my-enterprise"
  team_id         = 123456
  username        = "octocat"
}
