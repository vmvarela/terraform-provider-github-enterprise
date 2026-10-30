# Terraform Provider for GitHub Enterprise

Use Terraform or OpenTofu to manage GitHub Enterprise Cloud teams and cost centers without taking over your repositories and organizations. Keep those in [`integrations/github`](https://registry.terraform.io/providers/integrations/github/latest/docs); use this provider for the enterprise-level gaps.

Version **0.1.0** supports enterprise teams, direct membership and organization assignments. Version **0.2.0** adds cost centers and their billing assignments. Check [releases](https://github.com/vmvarela/terraform-provider-github-enterprise/releases) for published builds. GitHub Enterprise Server is not supported.

Version **0.3.0** adds read-only SCIM user and group lookups for Enterprise Managed Users. These data sources are not available in 0.2.x; provisioning and membership stay with your identity provider (IdP).

Version **0.4.0** moves the HTTP layer onto `google/go-github` v93. It is a breaking release: the team resource and its data sources no longer expose `notification_setting`, because the library does not model it, and a SCIM attribute omitted by the IdP now reads as null or false rather than being distinguishable from an explicit empty value.

Start with the [quickstart](docs/guides/quickstart.md) if you want one page from token to a working team.

## Create a team

Use a published 0.4.x release for this example. Save it as `main.tf`, replace `my-enterprise` with your enterprise slug, and set a token as described under [Authentication](#authentication). The team starts without organization assignments:

```hcl
terraform {
  required_providers {
    github-enterprise = {
      source  = "vmvarela/github-enterprise"
      version = "~> 0.4.0"
    }
  }
}

provider "github-enterprise" {
  enterprise_slug = "my-enterprise"
}

resource "github_enterprise_team" "platform" {
  provider = github-enterprise

  name                        = "Platform"
  organization_selection_type = "disabled"
}
```

Download the provider and inspect the proposed changes before applying them:

```sh
terraform init
terraform plan
```

Use `tofu` instead of `terraform` for OpenTofu. The constraint stays within 0.4.x; commit the lock file to keep the selected version reproducible. See the [complete example](examples/complete) for both providers, membership and organization assignments in one configuration.

**Keep `provider = github-enterprise` on every resource and data source.** The `github_enterprise_` prefix otherwise makes Terraform select `integrations/github`, which shares that prefix.

Set `enterprise_slug` once on the provider, as above, or through `GITHUB_ENTERPRISE_SLUG`. Resources and data sources inherit it; set it on an individual block only to override that default. Changing the resolved enterprise replaces the resources bound to it; it does not move them.

## Authentication

Set `GITHUB_ENTERPRISE_TOKEN` in your shell rather than putting a token in configuration. The provider falls back to `GITHUB_TOKEN`; an explicit sensitive `token` argument overrides both. The HTTP client does not log credentials.

- **Teams:** use a classic PAT with `read:enterprise` for reads or `admin:enterprise` for writes, held by a user with the required enterprise role.
- **Cost centers:** use a classic PAT or OAuth token with `manage_billing:enterprise`. Use an enterprise owner or billing manager: organization-owner visibility is too narrow to check billing conflicts safely.
- **SCIM:** requires Enterprise Managed Users. Use a setup-user classic PAT with `scim:enterprise`, or another enterprise owner's classic PAT with `admin:enterprise` for GET requests. GitHub also documents App user and installation tokens with **Enterprise SCIM: read**, not live-validated here. Fine-grained PAT support is not asserted. See [GitHub's SCIM authentication requirements](https://docs.github.com/en/enterprise-cloud@latest/rest/enterprise-admin/scim#authentication).
- **GitHub Apps:** supply an already-issued token; this provider does not mint or refresh it. Teams require **Enterprise teams: read** or **write**. The App flow is not live-validated, and [App billing access is not generally available](https://docs.github.com/en/enterprise-cloud@latest/rest/billing/cost-centers#authentication). Permission tables alone do not prove your enterprise can grant that access.

Fine-grained PATs cannot call the cost-center endpoints; their compatibility with team endpoints is not asserted. The default endpoint is `https://api.github.com/`, with API version `2026-03-10`. For data residency, set `base_url` or `GITHUB_ENTERPRISE_BASE_URL` to your `https://api.YOUR-SUBDOMAIN.ghe.com/` endpoint; that path is not live-validated.

## Ownership and lifecycle

Choose a resource by what it owns, not just by its name:

| Resource | What Terraform owns |
| --- | --- |
| [`team`](docs/resources/team.md) | One enterprise team. Relationships use its stable numeric ID, not its slug. |
| [`team_membership`](docs/resources/team_membership.md) | One direct member; other members stay untouched. Let the IdP own membership of IdP-linked teams. |
| [`team_organizations`](docs/resources/team_organizations.md) | **All** organization assignments for a team in `selected` mode. Omitted organizations are removed. |
| [`cost_center`](docs/resources/cost_center.md) | The name of one cost center, not its assignments. Destroy archives it and releases its assignments; there is no hard delete. |
| [`cost_center_resources`](docs/resources/cost_center_resources.md) | **All** users, organizations, repositories and enterprise teams assigned to one cost center. Omitted categories are empty. |

**Import existing assignments before managing them. Destroying either assignment resource removes its entire current set, including additions made outside Terraform.** Use one binding per remote object, never overlapping bindings across providers or an IdP. Use the [data sources](docs/data-sources) to read existing objects without owning them.

Changing an IdP group link can replace direct membership on GitHub. That lifecycle is not live-validated; do not rely on it until you have tested it with a safe fixture.

SCIM lookups expose opaque, case-sensitive `scim_group_id` and `scim_user_id` strings. Use `scim_group_id`, **not** the data source's composite Terraform `id`, for a team's `group_id`; neither is an organization's numeric external-group ID. List lookups follow SCIM pagination and accept one API filter. They are not point-in-time snapshots: concurrent IdP changes can invalidate a listing. Names and identities read into Terraform state may contain personal information; protect that state accordingly.

Cost-center additions check other active centers and refuse known conflicts rather than silently moving billing. This requires enterprise-wide visibility and cannot prevent concurrent external changes. Partial failures retain recoverable state; inspect an unexpected reassignment before retrying. Unknown resource types stop assignment reconciliation rather than being discarded.

Supply the exact identifiers accepted by GitHub's billing API. Repository and enterprise-team identifier formats remain unverified; organization assignment, import and archival are covered by the live suite. Budgets, analytics and AI credit pool configuration are outside the scope.

GitHub can hide permission failures behind a 404. Check token access before accepting a plan that recreates a missing object. Explicit permission, rate-limit and server errors stop refresh without dropping state. Writes are not automatically retried: replaying an uncertain create can duplicate an object.

The [compatibility contract](docs/guides/initial-contract.md) covers incomplete API responses, group links, drift and deprecation. Its live-validated scope is enterprise teams, read-only SCIM lookups and cost centers with a classic PAT against `api.github.com`, alongside the official provider. It does not cover App tokens, IdP group lifecycle or data-residency hosts. CI exercises Terraform 1.14.3 and OpenTofu 1.11.0 on Linux, not every CLI version or target platform.

## Import and migration

Create matching configuration, then import the existing identity rather than recreating it. For a team and, from 0.2.0, a cost center's assignments:

```sh
terraform import github_enterprise_team.platform my-enterprise/123456
terraform import github_enterprise_cost_center_resources.platform my-enterprise/COST_CENTER_ID
```

Team IDs are numeric; cost-center IDs are opaque strings that survive renames. See each resource's reference for its import format. When migrating from `vmvarela/github`, import here, compare the plan, then remove the old bindings **without destroying the objects** (`removed` with `destroy = false`, or `terraform state rm` with a backup). `state replace-provider` alone cannot handle renamed types and different schemas. Do not apply while both bindings exist.

## Development

With Go 1.26.6 or newer and Terraform on `PATH` (or `TF_ACC_TERRAFORM_PATH` set), build and check the provider locally:

```sh
make check
make build
make docs
```

To use that binary, point `TF_CLI_CONFIG_FILE` at a dedicated file with your absolute `bin` path:

```hcl
provider_installation {
  dev_overrides {
    "vmvarela/github-enterprise" = "/absolute/path/to/terraform-provider-github-enterprise/bin"
  }
  direct {}
}
```

With this override, `init` still resolves the published provider; run `plan` directly once any other providers are installed. That command calls GitHub, so use an appropriate test configuration. Local HTTP tests need no credentials; real acceptance tests require explicit gates and a dedicated enterprise. See [CONTRIBUTING.md](CONTRIBUTING.md) before running them.

Packages are signed, and published versions are immutable. Use the [release verification guide](docs/guides/releases.md) to check a download; maintainers follow [RELEASING.md](RELEASING.md).

This is an independent Terraform Plugin Framework implementation, originating in [the Enterprise Teams contribution](https://github.com/integrations/terraform-provider-github/pull/3008). See [NOTICE](NOTICE) and [LICENSE](LICENSE) for attribution and terms.
