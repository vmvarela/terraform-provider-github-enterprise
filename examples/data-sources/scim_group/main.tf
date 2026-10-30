data "github_enterprise_scim_group" "example" {
  provider = github-enterprise

  scim_group_id = "example-group-id"
}
