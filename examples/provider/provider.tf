terraform {
  required_providers {
    github-enterprise = {
      source = "vmvarela/github-enterprise"
    }
  }
}

provider "github-enterprise" {
  # Set the enterprise once; resources inherit it. Defaults to
  # GITHUB_ENTERPRISE_SLUG, and the token defaults to GITHUB_ENTERPRISE_TOKEN.
  enterprise_slug = "my-enterprise"
}

# The official provider also declares github_* types, so every block states
# provider = github-enterprise explicitly.
resource "github_enterprise_team" "platform" {
  provider = github-enterprise

  name = "Platform"
}
