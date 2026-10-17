data "github_enterprise_team" "example" {
  provider = github-enterprise

  enterprise_slug = "my-enterprise"
  slug            = "ent:platform"
}
