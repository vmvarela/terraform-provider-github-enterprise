data "githubenterprise_team_membership" "example" {
  enterprise_slug = "my-enterprise"
  team_id         = 123456
  username        = "octocat"
}
