---
page_title: "Compatibility and lifecycle"
description: |-
  Ownership, drift behaviour, import requirements and compatibility commitments for the v0.2 release line.
---

# Compatibility and lifecycle

This page defines what each resource owns, how the provider behaves when GitHub
returns incomplete data, and which compatibility commitments apply to the v0.2
release line. Release mechanics are covered by the
[release verification guide](https://github.com/vmvarela/terraform-provider-github-enterprise/blob/master/docs/guides/releases.md).

The provider complements `integrations/github` with enterprise teams and cost centers
missing from the official provider. Its source is `vmvarela/github-enterprise`, its
local name is `github-enterprise`, and resource and data source types start with
`github_enterprise_`. The prefix is shared with some official resources: every
resource and data source must explicitly select `provider = github-enterprise`, or
Terraform infers the official provider from the type name.

## Scope and ownership

| Resource | Ownership | Import ID |
| --- | --- | --- |
| `github_enterprise_team` | One enterprise team | `enterprise_slug/team_id` |
| `github_enterprise_team_membership` | One direct member; other members are untouched | `enterprise_slug/team_id/username` |
| `github_enterprise_team_organizations` | The complete organization assignment set | `enterprise_slug/team_id` |
| `github_enterprise_cost_center` (from v0.2.0) | One cost center; destroy archives it | `enterprise_slug/cost_center_id` |
| `github_enterprise_cost_center_resources` (from v0.2.0) | The complete cost-center assignment set across all supported categories | `enterprise_slug/cost_center_id` |

Read-only data sources are `github_enterprise_team`, `github_enterprise_teams`,
`github_enterprise_team_membership`, `github_enterprise_team_organizations`,
`github_enterprise_cost_center`, and `github_enterprise_cost_centers`.
A missing single team or membership is an error; list lookups may return empty
collections. An existing membership reports `is_member = true`.

## Identity and drift

Team relationships use positive numeric team IDs, never slugs. Slugs may change during
renames and may be reused by another team. A failed slug lookup after a positive
ID match is inconclusive: retry the operation, retaining the existing state.
GitHub can mask authorization failures as 404, so verify token access before
applying a plan that proposes recreating missing resources.

Team creation defaults to `organization_selection_type = "disabled"` and
`notification_setting = "notifications_enabled"`. These are request defaults, not
substitutes for missing remote values. Refresh and import fail without changing
state if GitHub omits either setting. Data sources expose omitted settings as
`null`. Assignment creation and updates require an observed `selected` mode,
because `selected` cannot be verified otherwise.

Removing a description clears it. An empty `group_id` unlinks the IdP group using
an explicit JSON null. Unrelated updates do not resend `group_id`. Direct members
must not be managed by Terraform while the IdP owns the team's membership, and
changing a group link may replace direct membership on GitHub.

Only one assignment resource may own a team. Import existing assignments before
managing them. Omitted organizations are removed; an empty set removes all.
Destroy removes all current assignments, including changes made outside Terraform.
Partial failures retain observed assignments so a later plan can reconcile them.

### Cost centers from v0.2.0

Cost centers are available from v0.2.0, not in v0.1.0. They have opaque string IDs,
not numeric team IDs. Renaming preserves identity; archiving is not a hard delete.
Refresh treats an explicitly deleted cost center as absent. The single/list
`github_enterprise_cost_center` and `github_enterprise_cost_centers` data sources
expose the API's raw resource types and names.

The cost-center resource manages its name only. One `cost_center_resources`
resource owns all four assignment categories: `users`, `organizations`,
`repositories` and `enterprise_teams`. Omitted categories are empty. Import
existing assignments first; destroy removes every current assignment, including
out-of-band additions. Unknown resource types prevent reconciliation rather than
being silently ignored. Do not create overlapping Terraform bindings.

Adding a resource can move its billing from another cost center. The provider
checks other active centers before additions and rejects observed conflicts.
Use an enterprise owner or billing manager with visibility of the whole
enterprise; limited organization-owner visibility cannot establish exclusivity.
Concurrent external changes are not atomic with this check. Unexpected
reassignments and partial failures require inspection, not blind retries.

Supply exact billing API resource identifiers. The public API does not fully
specify repository and enterprise-team formats, and their live behavior has not
been verified. Budgets, analytics and AI credit pool settings are not managed.

Cost centers require a classic PAT or OAuth access token with
`manage_billing:enterprise`. Fine-grained PATs cannot call these endpoints.
GitHub App billing authentication is not generally available: generated
permission tables do not prove that the enterprise can grant Enterprise billing
permission. Verify availability and the required operations before using an App.
See the [API authentication notice](https://docs.github.com/en/enterprise-cloud@latest/rest/billing/cost-centers#authentication).

## Compatibility

The provider targets GitHub Enterprise Cloud. No GitHub Enterprise Server support
is claimed. Cost centers were added in v0.2.0; SCIM and billing usage data sources
remain outside its scope. The default API version is `2026-03-10`.

Packages are built for Linux, macOS and Windows on amd64 and arm64. Releases are
validated with Terraform 1.14.3 and OpenTofu 1.11.0 on Linux; cross-compilation
does not establish runtime acceptance on the other platforms.

Authentication verified against `api.github.com` uses a classic PAT, with
`read:enterprise` for reads and `admin:enterprise` for writes. An already-issued
GitHub App token with **Enterprise teams: read** or **write** is accepted, but
minting and refreshing installation tokens are the caller's responsibility and the
App-token flow has not been validated live. Fine-grained PAT compatibility is not
asserted. Data-residency hosts are supported through `base_url` or
`GITHUB_ENTERPRISE_BASE_URL` but have not been exercised against a live enterprise.

Coexistence with `integrations/github` 6.13.0 was verified against a test
enterprise: the [complete
example](https://github.com/vmvarela/terraform-provider-github-enterprise/tree/master/examples/complete)
planned, applied and destroyed a team, its direct membership and one organization
assignment while the official provider read the enterprise account, leaving no
residue. Import creates a binding; it does not transfer ownership from another
provider. Never apply two bindings that manage the same remote object.

## Deprecation and removal

A resource or data source is marked deprecated for at least one published minor
release before it is removed, and the removal version is announced in the release
notes. Before 1.0, removal may occur in a later minor release; after 1.0, removal
requires a major version. Existing configurations keep working during deprecation.

When the official provider gains an equivalent capability, the provider publishes a
migration path that names the minimum official version, compares schemas and
ownership, and explains how to import without deleting remote objects. Do not
assume `state replace-provider` is sufficient across different schemas.

Within v0.1.x the provider preserves resource names, attribute meanings, defaults,
ownership and import formats. Any necessary incompatible correction requires a
documented migration and a new minor version.
