data "github_enterprise_scim_user" "example" {
  provider = github-enterprise

  scim_user_id = "example-user-id"
}
