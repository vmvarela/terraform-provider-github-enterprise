data "github_enterprise_scim_group" "example" {
  provider = github-enterprise

  scim_group_id = "example-group-id"
}

# Look the group up by its IdP name instead of copying the raw SCIM ID. The
# lookup fails when more than one group matches, so it never picks one silently.
data "github_enterprise_scim_group" "by_name" {
  provider = github-enterprise

  display_name = "Platform"
}
