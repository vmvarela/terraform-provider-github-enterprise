data "github_enterprise_scim_groups" "example" {
  provider = github-enterprise

  filter = "displayName eq \"Platform\""
}
