# Terraform Provider for GitHub Enterprise

A focused Terraform and OpenTofu provider for GitHub Enterprise Cloud administration. Use it alongside [`integrations/github`](https://registry.terraform.io/providers/integrations/github/latest/docs): keep repositories and organizations there, and manage enterprise teams here.

**Status:** initial implementation, not yet published to the Terraform Registry. HTTP-backed Terraform integration tests run without credentials. Real Enterprise Cloud acceptance tests are opt-in; production use should wait for validation in a test enterprise.

## Scope

| Area | Resources | Data sources |
| --- | --- | --- |
| Enterprise teams | `github_enterprise_team` | `github_enterprise_team`, `github_enterprise_teams` |
| Direct membership | `github_enterprise_team_membership` | `github_enterprise_team_membership` |
| Organization assignments | `github_enterprise_team_organizations` | `github_enterprise_team_organizations` |

All relations use a numeric `team_id`, which survives team renames. Team reads verify numeric identity before trusting a stored slug, including when someone reuses an old slug for a different team. If a team appears in the ID lookup but its slug disappears before the detail request, the operation fails and retains state so it can be retried.

Future work: enterprise cost centers, read-only SCIM lookups, and selected billing data sources. Existing enterprise functionality in `integrations/github` stays there. No GitHub Enterprise Server compatibility is claimed in this initial version.

## Example

After the provider is published, use the following source address. Until then, see local development below.

```hcl
terraform {
  required_providers {
    github = {
      source = "integrations/github"
    }
    github-enterprise = {
      source = "vmvarela/github-enterprise"
    }
  }
}

provider "github-enterprise" {}

resource "github_enterprise_team" "platform" {
  provider = github-enterprise

  enterprise_slug             = "my-enterprise"
  name                        = "Platform"
  description                 = "Platform Engineering"
  organization_selection_type = "selected"
}

resource "github_enterprise_team_membership" "member" {
  provider = github-enterprise

  enterprise_slug = "my-enterprise"
  team_id         = github_enterprise_team.platform.team_id
  username        = "octocat"
}

resource "github_enterprise_team_organizations" "platform" {
  provider = github-enterprise

  enterprise_slug    = "my-enterprise"
  team_id            = github_enterprise_team.platform.team_id
  organization_slugs = ["my-organization"]
}
```

`github-enterprise` is the local provider name and distribution name. Resource and data source types use the readable `github_enterprise_` prefix. Set `provider = github-enterprise` on every resource and data source: otherwise Terraform infers `github` from the type name and selects the official provider. Configure the local name explicitly as above.

## Authentication

```sh
export GITHUB_ENTERPRISE_TOKEN='...'
```

The provider reads `GITHUB_ENTERPRISE_TOKEN`, falling back to `GITHUB_TOKEN`. An explicit sensitive `token` provider argument takes precedence. Credentials are never logged by the HTTP client.

Supported token flows:

- Classic PAT: `read:enterprise` for reads, `admin:enterprise` for writes, held by a user with the required enterprise role.
- GitHub App installed on the enterprise: supply its access token with **Enterprise teams: read** or **write**, as appropriate. The provider accepts an already-issued token; minting and refreshing installation tokens are the caller's responsibility.

GitHub's endpoint-specific token tables and [Enterprise Teams GA announcement](https://github.blog/changelog/2026-06-04-enterprise-teams-is-now-generally-available/) document GitHub App support. Some introductory API text still says Apps are unsupported. Verify authorization against the actual endpoint and enterprise before deployment; fine-grained PAT compatibility is not asserted by this initial implementation.

For Enterprise Cloud with data residency:

```hcl
provider "github-enterprise" {
  base_url = "https://api.YOUR-SUBDOMAIN.ghe.com/"
}
```

`GITHUB_ENTERPRISE_BASE_URL` is also supported. The default is `https://api.github.com/`. `api_version` defaults to `2026-03-10`.

## Ownership and lifecycle

- Team refresh and import require GitHub to return `organization_selection_type` and `notification_setting`. If either is absent, refresh fails and retains state. The team data sources return `null` for omitted settings. Creation defaults apply to requests only; they are never substituted for missing API values. Organization assignment creation and updates also fail if GitHub omits the selection mode, because `selected` cannot be verified.
- Membership resources manage one direct member and leave other members alone. Let the IdP control membership of an IdP-linked team; use the membership data source for reads.
- `team_organizations` owns the **entire** assignment set. Omitted organizations are removed. An empty set removes all assignments. Use one instance per team, with `organization_selection_type = "selected"`.
- Existing assignments must be imported before managing them. Destroying `team_organizations` removes every current assignment, including ones added outside Terraform.
- Removing `group_id` sends JSON `null`; changing a group link may replace direct membership on GitHub. This API behavior requires live acceptance validation. Removing a description sends an empty string.
- Do not manage the same remote object through this provider and another provider or IdP simultaneously.
- The client uses 60-second HTTP timeouts and follows all pagination pages. It does not automatically retry writes, because replaying a create after an uncertain network failure can duplicate a team. Rate-limit diagnostics retain the request ID and `Retry-After` header; rerun after the limit resets.
- GitHub can mask authorization failures as 404. Verify token access before applying a plan that proposes recreating missing resources. Explicit 403/429/5xx errors fail refresh without deleting state.

## Import and migration

```sh
terraform import github_enterprise_team.platform my-enterprise/123456
terraform import github_enterprise_team_membership.member my-enterprise/123456/octocat
terraform import github_enterprise_team_organizations.platform my-enterprise/123456
```

When moving from the author's `vmvarela/github` fork, create matching configuration here and import the existing objects into these resources. Compare a plan, then remove the old resource bindings **without destroying the remote objects**. Use `removed` blocks with `destroy = false` where supported, or carefully use `terraform state rm` with a state backup. Do not apply while both providers manage the same object. Renamed resource types and schemas mean `state replace-provider` alone is insufficient.

## Development

Requires Go 1.26.6 or newer. CI tests Terraform 1.14.3 and OpenTofu 1.11.0. Other CLI versions have not been validated.

```sh
make build
make test
make integration  # Terraform must be on PATH, or set TF_ACC_TERRAFORM_PATH
make docs         # Regenerate Registry documentation from schemas and examples
```

For a local binary, add the absolute path to `bin` in a dedicated CLI configuration:

```hcl
provider_installation {
  dev_overrides {
    "vmvarela/github-enterprise" = "/absolute/path/to/terraform-provider-github-enterprise/bin"
  }
  direct {}
}
```

Set `TF_CLI_CONFIG_FILE` to that file. With the development override, run `terraform plan` or `tofu plan` directly; `init` would try to resolve the unpublished provider in the Registry. Other providers in the configuration must already be installed.

Real API tests require a dedicated test enterprise, an appropriately privileged token and explicit opt-in:

```sh
TF_ACC=1 GITHUB_ENTERPRISE_SLUG=my-test-enterprise \
  go test ./internal/provider -run '^TestAcc' -v
```

These tests create and delete a uniquely named test team. They are never run against real enterprises by pull-request CI.

## Releases

The tag-triggered release workflow builds signed Terraform-compatible archives using GoReleaser. Before tagging a release, configure `GPG_PRIVATE_KEY`, `GPG_PASSPHRASE`, and `GPG_FINGERPRINT`, register the public signing key, and publish the provider through the Terraform Registry UI. Registry registration and signing credentials are not configured by the initial implementation. OpenTofu Registry registration is a separate follow-up.

## Origin

The initial scope and lifecycle safeguards derive from [vmvarela's Enterprise Teams contribution](https://github.com/integrations/terraform-provider-github/pull/3008). This repository implements an independent provider using Terraform Plugin Framework rather than copying the full SDKv2 provider. Upstream contributions can continue independently.

See [CONTRIBUTING.md](CONTRIBUTING.md), [NOTICE](NOTICE) and [LICENSE](LICENSE).
