data "github_enterprise_budgets" "user_budgets" {
  provider = github-enterprise

  scope = "user"
}

data "github_enterprise_budgets" "all" {
  provider = github-enterprise
}
