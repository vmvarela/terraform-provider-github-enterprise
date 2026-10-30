data "github_enterprise_scim_users" "example" {
  provider = github-enterprise

  filter = "userName eq \"example-user\""
}
