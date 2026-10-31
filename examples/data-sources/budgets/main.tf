data "github_enterprise_budgets" "user_budgets" {
  provider = github-enterprise

  budget_scope = "user"
}

data "github_enterprise_budgets" "all" {
  provider = github-enterprise
}
