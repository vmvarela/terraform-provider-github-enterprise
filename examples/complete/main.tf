terraform {
  required_providers {
    github = {
      source  = "integrations/github"
      version = "= 6.13.0"
    }
    github-enterprise = {
      source  = "vmvarela/github-enterprise"
      version = "~> 0.5"
    }
  }
}

provider "github" {}

# Set the enterprise once here; every resource below inherits it. A value set on
# an individual resource or data source wins over this one.
provider "github-enterprise" {
  enterprise_slug = var.enterprise_slug
}

variable "enterprise_slug" {
  type = string
}

variable "username" {
  type = string
}

variable "organization_slugs" {
  type = set(string)
}

# The official provider owns this lookup, despite its github_enterprise_ prefix.
data "github_enterprise" "account" {
  provider = github
  slug     = var.enterprise_slug
}

resource "github_enterprise_team" "platform" {
  provider = github-enterprise

  name                        = "Platform"
  description                 = "Platform Engineering"
  organization_selection_type = "selected"
}

resource "github_enterprise_team_membership" "member" {
  provider = github-enterprise

  team_id  = github_enterprise_team.platform.team_id
  username = var.username
}

resource "github_enterprise_team_organizations" "platform" {
  provider = github-enterprise

  team_id            = github_enterprise_team.platform.team_id
  organization_slugs = var.organization_slugs
}

data "github_enterprise_team" "platform" {
  provider = github-enterprise

  slug = github_enterprise_team.platform.slug
}

data "github_enterprise_teams" "all" {
  provider = github-enterprise

  depends_on = [github_enterprise_team.platform]
}

data "github_enterprise_team_membership" "member" {
  provider = github-enterprise

  team_id  = github_enterprise_team.platform.team_id
  username = github_enterprise_team_membership.member.username
}

data "github_enterprise_team_organizations" "platform" {
  provider = github-enterprise

  team_id    = github_enterprise_team.platform.team_id
  depends_on = [github_enterprise_team_organizations.platform]
}
