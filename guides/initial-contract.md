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

Classic PATs and enterprise-installed GitHub App tokens are the intended token
flows. Live lifecycle, permissions, optional response fields, IdP behavior and
data-residency coverage remain release gates in issue #4. Fixture tests and
package smoke tests do not establish these live API contracts. Document verified
coverage before the first release; narrow unsupported claims if an appropriate
test environment is unavailable.

The [complete example](../examples/complete) is checked with both providers loaded.
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
