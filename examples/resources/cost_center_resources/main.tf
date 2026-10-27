# Owns the ENTIRE assignment set of the cost center; destroying it removes all assignments.
resource "github_enterprise_cost_center_resources" "example" {
  provider = github-enterprise

  cost_center_id = github_enterprise_cost_center.example.cost_center_id

  # Placeholders: supply the exact names the GitHub API accepts. Formats are unverified here.
  users            = var.cost_center_users
  organizations    = var.cost_center_organizations
  repositories     = var.cost_center_repositories
  enterprise_teams = var.cost_center_enterprise_teams
}

variable "cost_center_users" { type = set(string) }
variable "cost_center_organizations" { type = set(string) }
variable "cost_center_repositories" { type = set(string) }
variable "cost_center_enterprise_teams" { type = set(string) }
