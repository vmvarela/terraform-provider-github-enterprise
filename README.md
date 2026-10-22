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

Documented token flows:

- Classic PAT: `read:enterprise` for reads, `admin:enterprise` for writes, held by a user with the required enterprise role.
- GitHub App installed on the enterprise: supply its access token with **Enterprise teams: read** or **write**, as appropriate. The provider accepts an already-issued token; minting and refreshing installation tokens are the caller's responsibility. This flow has not been live-validated.

The initial verified authentication scope is a classic PAT against `api.github.com`. GitHub's endpoint-specific token tables and [Enterprise Teams GA announcement](https://github.blog/changelog/2026-06-04-enterprise-teams-is-now-generally-available/) document GitHub App support, but this provider's App-token flow remains outside the verified scope. Some introductory API text still says Apps are unsupported. Fine-grained PAT compatibility is not asserted.

Configuration example for Enterprise Cloud with data residency (not live-validated):

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
- `group_id` is exposed: unrelated updates omit it and explicit unlink sends JSON `null` (covered by fixture tests, not live validation). IdP group link/change/unlink and membership effects are outside the initial support contract. Changing a group link may replace direct membership on GitHub; do not rely on this lifecycle until it has been validated against a safe EMU fixture. Removing a description sends an empty string.
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

### Live acceptance

Live tests are opt-in and fail closed; pull-request CI never runs them. Gate every invocation per command, with the confirmation in the same shell — never recorded in `.envrc` or shell profiles:

```sh
TF_ACC=1 GITHUB_ENTERPRISE_SLUG=my-test-enterprise \
  GITHUB_ENTERPRISE_TEST_CONFIRM=my-test-enterprise \
  go test ./internal/provider -run '^TestAccTeam$' -v
```

- `TF_ACC` must be exactly `1`; any other value (including `0`) skips the tests.
- `GITHUB_ENTERPRISE_TEST_CONFIRM` must exactly match `GITHUB_ENTERPRISE_SLUG`, naming the intended enterprise in the same command.
- The configured `GITHUB_ENTERPRISE_BASE_URL` must be GitHub Enterprise Cloud (`api.github.com` or `*.ghe.com`); loopback and other hosts are refused before any request.

Suites and required fixtures:

- `TestAccTeam` (core): creates a uniquely named `tf-acc-*` team, reads it through both team data sources, reconciles an out-of-band rename back to the configured name, renames it, clears its description, imports it and verifies the numeric ID stays stable. No `group_id` is involved.
- `TestAccTeamMembership`: additionally requires `GITHUB_ENTERPRISE_TEST_USERNAME=<user>` plus `GITHUB_ENTERPRISE_TEST_USER_CONFIRM` set to exactly that username, confirming the user is a disposable, approved fixture. The team is created and verified against the baseline before the membership is written; an out-of-band removal of that membership is reconciled. Adds, reads, imports and removes the membership on the test-created team only.
- `TestAccTeamOrganizations`: additionally requires `GITHUB_ENTERPRISE_TEST_ORG_A=<slug>`, `GITHUB_ENTERPRISE_TEST_ORG_B=<slug>` (distinct) plus `GITHUB_ENTERPRISE_TEST_ORGS_CONFIRM` set to exactly `<ORG_A>,<ORG_B>`, confirming both are dedicated test organizations. Exercises assignment drift reconciliation, authoritative set removal (`[A,B]` → `[A]`), the empty set and resource deletion on the test-created team only.

Safety model: a read-only listing of existing team IDs is captured before creation, and only a team whose numeric ID is absent from that baseline is accepted; destroy verification checks that exact ID is gone. Teams are never adopted or deleted by name, and import steps use only IDs from the same test's state. There is no sweeper: if the test process is killed during a network-ambiguous step, the team can remain — inspect the enterprise and resolve it manually rather than guessing. All three suites passed against `api.github.com` using the configured classic PAT and API version `2026-03-10`; see the [initial contract](guides/initial-contract.md) for the remaining release gates. GitHub App tokens, IdP group lifecycle, data-residency hosts, and live coexistence with `integrations/github` remain outside the verified initial scope.

## Releases

The [initial contract](guides/initial-contract.md) defines compatibility, imports and the deprecation policy when the official provider gains an equivalent feature. See the [complete coexistence example](examples/complete) and [release validation guide](guides/releases.md).

Every release runs the reusable CI workflow against the tagged commit before publication. CI also builds a snapshot with a disposable signing key, verifies its archives and signature, and installs the packaged provider with both Terraform and OpenTofu.

The tag-triggered release workflow builds signed Terraform-compatible archives using GoReleaser. Before tagging a release, configure `GPG_PRIVATE_KEY`, `GPG_PASSPHRASE`, and `GPG_FINGERPRINT`, register the public signing key, and publish the provider through the Terraform Registry UI. Registry registration and signing credentials are not configured by the initial implementation. OpenTofu Registry registration is a separate follow-up.

## Origin

The initial scope and lifecycle safeguards derive from [vmvarela's Enterprise Teams contribution](https://github.com/integrations/terraform-provider-github/pull/3008). This repository implements an independent provider using Terraform Plugin Framework rather than copying the full SDKv2 provider. Upstream contributions can continue independently.

See [CONTRIBUTING.md](CONTRIBUTING.md), [NOTICE](NOTICE) and [LICENSE](LICENSE).
