---
page_title: "Initial release contract"
description: |-
  Scope, ownership, compatibility and deprecation commitments for the v0.1 release line.
---

# Initial release contract

The v0.1 release line complements `integrations/github` with Enterprise Teams
capabilities missing from the official provider. Its source is
`vmvarela/github-enterprise`, its local name is `github-enterprise`, and resource
and data source types start with `github_enterprise_`. The prefix is shared with
some official resources: every example must explicitly select its provider.

## Scope and ownership

| Resource | Ownership | Import ID |
| --- | --- | --- |
| `github_enterprise_team` | One enterprise team | `enterprise_slug/team_id` |
| `github_enterprise_team_membership` | One direct member; other members are untouched | `enterprise_slug/team_id/username` |
| `github_enterprise_team_organizations` | The complete organization assignment set | `enterprise_slug/team_id` |

Read-only data sources are `github_enterprise_team`, `github_enterprise_teams`,
`github_enterprise_team_membership`, and `github_enterprise_team_organizations`.
A missing single team or membership is an error; list lookups may return empty
collections. An existing membership reports `is_member = true`.

Relationships use positive numeric team IDs, never slugs. Slugs may change during
renames and may be reused by another team. A failed slug lookup after a positive
ID match is inconclusive: retry the operation, retaining the existing state.

Team creation defaults to `organization_selection_type = "disabled"` and
`notification_setting = "notifications_enabled"`. These are request defaults, not
substitutes for missing remote values. Refresh and import fail without changing
state if GitHub omits either setting. Data sources expose omitted settings as
`null`. Assignment creation and updates require an observed `selected` mode.

Removing a description clears it. An empty `group_id` unlinks the IdP group using
an explicit JSON null. Unrelated updates do not resend `group_id`. Direct members
must not be managed by Terraform while the IdP owns the team's membership.

Only one assignment resource may own a team. Import existing assignments before
managing them. Omitted organizations are removed; an empty set removes all.
Destroy removes all current assignments, including changes made outside Terraform.
Partial failures retain observed assignments so a later plan can reconcile them.

## Compatibility and release readiness

The initial target is GitHub Enterprise Cloud. No GitHub Enterprise Server support
is claimed. Cost centers, SCIM and billing queries belong to later milestones.
The default API version is `2026-03-10`. CI exercises Terraform 1.14.3 and OpenTofu
1.11.0 on Linux; packages are built for Linux, macOS and Windows, amd64 and arm64.
Cross-compilation does not establish runtime acceptance on all six platforms.

The initial verified authentication scope is a classic PAT against
`api.github.com`. The gated live acceptance suites in `internal/provider/provider_test.go`
are the instrument for issue #4: they require `TF_ACC=1` plus a per-command
`GITHUB_ENTERPRISE_TEST_CONFIRM` exactly matching the enterprise slug, and they
refuse any API host other than Enterprise Cloud before writing. On 2026-10-19,
all three suites passed against the public Enterprise Cloud API (`api.github.com`)
using the configured classic PAT and API version `2026-03-10`. Together they
covered all three resources and four data sources, including imports, drift,
authoritative organization assignments, and cleanup of test-created teams.
GitHub App tokens, data-residency hosts, and runtime coexistence with
`integrations/github` were not tested and are outside the initial scope. The
`group_id` field remains exposed, but IdP group link/change/unlink and their
membership effects are unverified and outside the initial support commitment;
do not rely on that lifecycle until it is validated with a safe EMU fixture.
Offline checks (package and schema validation plus HTTP-fixture lifecycle tests)
verify contracts locally but do not establish live API contracts. The verified
scope and the areas left unverified are recorded in issue #4 and summarized in
the README.

The [complete example](https://github.com/vmvarela/terraform-provider-github-enterprise/tree/master/examples/complete) documents running with both
providers loaded; CI checks its formatting and installs the packaged provider
under both CLIs, but live runtime coexistence with `integrations/github` is
unverified.
Import creates a binding; it does not transfer ownership from another provider.
Never apply two bindings that manage the same remote object.

## When the official provider adds an equivalent

Check released functionality and lifecycle semantics, not just an upstream PR or
matching name. Stop adding overlapping features once an equivalent is available.
Publish a migration guide naming the minimum official version, comparing schemas
and ownership, and explaining how to import without deleting remote objects.

Mark the resource or data source deprecated for at least one published minor
release before removing it. Announce the removal version in release notes. Before
1.0, removal may occur in a later minor release; after 1.0, removal requires a
major version. Existing configurations must continue working during deprecation.
Do not assume `state replace-provider` is sufficient across different schemas.

Within v0.1.x, preserve resource names, attribute meanings, defaults, ownership and
import formats. Any necessary incompatible correction requires a documented
migration and a new minor version.
