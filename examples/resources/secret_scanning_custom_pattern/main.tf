# Defines an enterprise secret scanning custom pattern. GitHub creates it as
# unpublished; publish it from the enterprise secret scanning settings in the
# GitHub UI, where push protection for the pattern is also configured.
# Destroying this resource deletes the pattern and resolves its alerts.
resource "github_enterprise_secret_scanning_custom_pattern" "example" {
  provider = github-enterprise

  name    = "example-api-token"
  pattern = "(?i)example[a-z0-9_]{32}"

  must_match = ["^example"]
}
