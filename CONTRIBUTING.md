# Contributing

Keep this provider focused on enterprise-level capabilities missing from
`integrations/github`. Reproduce a lifecycle or API behavior issue before changing
it.

## Before opening a pull request

Run `make check` and `make integration`. HTTP-backed Terraform integration tests
must not require real credentials. Add meaningful regression coverage for changes
to identity, import, pagination, drift, and destructive operations. Use a dedicated
enterprise for real acceptance tests, which are always opt-in.

## Design constraints

Use numeric team identities in relationships. Never trust a slug as immutable
identity. Do not silently turn API permission failures into missing resources.
Preserve recoverable state after partially applied assignment updates. Avoid
automatic retries of non-idempotent operations.

Keep schemas, examples and Registry documentation consistent. Document ownership
(individual or authoritative), import syntax, token permissions and compatibility
limits. Do not add resources for reporting alone without a concrete Terraform use
case.

The provider name is `github-enterprise`; resource and data source types start with
`github_enterprise_`. Examples must explicitly set `provider = github-enterprise` so
they work alongside the official `github` provider. Run `make docs` after changing
schemas or examples.

`docs/` is published to the Terraform and OpenTofu registries as-is, so it
describes the provider to its users and never the process of building or releasing
it. CI graphs, package recipes, signing-key custody, registry registration and
acceptance-test plumbing belong in `RELEASING.md` or here.

Use conventional commit titles (`feat:`, `fix:`, `docs:`, `test:`, `ci:`). Releases
are manual version tags; merging a pull request does not publish a release. See
[RELEASING.md](RELEASING.md) for the release procedure and
[the compatibility contract](docs/guides/initial-contract.md) for the commitments
each release must keep.

## Live acceptance tests

Live tests are opt-in and fail closed; pull-request CI never runs them. They write
to a real enterprise, so gate every invocation per command, with the confirmation
in the same shell and never recorded in `.envrc` or shell profiles:

```sh
TF_ACC=1 GITHUB_ENTERPRISE_SLUG=my-test-enterprise \
  GITHUB_ENTERPRISE_TEST_CONFIRM=my-test-enterprise \
  go test ./internal/provider -run '^TestAccTeam$' -v
```

- `TF_ACC` must be exactly `1`; any other value (including `0`) skips the tests.
- `GITHUB_ENTERPRISE_TEST_CONFIRM` must exactly match `GITHUB_ENTERPRISE_SLUG`,
  naming the intended enterprise in the same command.
- The configured `GITHUB_ENTERPRISE_BASE_URL` must be GitHub Enterprise Cloud
  (`api.github.com` or `*.ghe.com`); loopback and other hosts are refused before
  any request.

Suites and required fixtures:

- `TestAccTeam` (core): creates a uniquely named `tf-acc-*` team, reads it through
  both team data sources, reconciles an out-of-band rename back to the configured
  name, renames it, clears its description, imports it and verifies the numeric ID
  stays stable. No `group_id` is involved.
- `TestAccTeamMembership`: additionally requires
  `GITHUB_ENTERPRISE_TEST_USERNAME=<user>` plus
  `GITHUB_ENTERPRISE_TEST_USER_CONFIRM` set to exactly that username, confirming
  the user is a disposable, approved fixture. The team is created and verified
  against the baseline before the membership is written; an out-of-band removal of
  that membership is reconciled. Adds, reads, imports and removes the membership on
  the test-created team only.
- `TestAccTeamOrganizations`: additionally requires
  `GITHUB_ENTERPRISE_TEST_ORG_A=<slug>`, `GITHUB_ENTERPRISE_TEST_ORG_B=<slug>`
  (distinct) plus `GITHUB_ENTERPRISE_TEST_ORGS_CONFIRM` set to exactly
  `<ORG_A>,<ORG_B>`, confirming both are dedicated test organizations. Exercises
  assignment drift reconciliation, authoritative set removal (`[A,B]` → `[A]`), the
  empty set and resource deletion on the test-created team only.
- `TestAccCostCenter`: creates a uniquely named cost center, reads both cost-center
  data sources, renames it, imports it and checks its stable opaque ID. Destroy
  archives it; **each run leaves an archived record**, not a hard-deleted object.
- `TestAccCostCenterResources`: additionally requires at least one disposable,
  initially unassigned billing fixture. Set any of
  `GITHUB_ENTERPRISE_TEST_CC_USER`, `GITHUB_ENTERPRISE_TEST_CC_ORG`,
  `GITHUB_ENTERPRISE_TEST_CC_REPO` or `GITHUB_ENTERPRISE_TEST_CC_TEAM`, with each
  supplied value repeated exactly in its matching `_CONFIRM` variable in the same
  command. Values must be the exact identifiers the billing API accepts; repository
  and enterprise-team formats remain unverified. The suite creates and verifies a
  new cost center before assigning fixtures, imports the assignment set, empties it
  and archives the test-created center. This changes real billing attribution:
  never use production users, organizations, repositories or teams as fixtures.

Cost-center suites require enterprise-wide billing visibility: use a classic PAT
with `manage_billing:enterprise` held by an enterprise owner or billing manager.
Organization-owner tokens cannot establish cross-center exclusivity. GitHub App
Enterprise billing permission is not generally available; verify that the target
enterprise can grant it and that the required operations work before using an App
token. Fine-grained PATs cannot call these endpoints. No cost-center live run has
been recorded yet.

For cost centers, capture the baseline of both active and deleted IDs before
creation and accept only a newly returned ID absent from it. Verify that exact ID
is archived and empty afterward; a test-only provider guard refuses to archive
baseline or empty IDs even during automatic teardown. Never adopt or clean up by
name. Preflight refuses
already-assigned fixtures but is not atomic with concurrent external writes. If a
run fails or is interrupted, inspect its exact ID, state and assignments manually;
do not rerun blindly or sweep unrelated cost centers.

Safety model: a read-only listing of existing team IDs is captured before creation,
and only a team whose numeric ID is absent from that baseline is accepted; destroy
verification checks that exact ID is gone. Teams are never adopted or deleted by
name, and import steps use only IDs from the same test's state. There is no
sweeper: if the test process is killed during a network-ambiguous step, the team can
remain — inspect the enterprise and resolve it manually rather than guessing.

All three team suites passed against `api.github.com` on 2026-10-19 using a classic PAT
and API version `2026-03-10`. Live coexistence with `integrations/github` 6.13.0
was verified on 2026-10-23 through `examples/complete`. GitHub App tokens, IdP group
lifecycle, and data-residency hosts remain unverified; record a new result here when
one of them is exercised.

## Overlap with the official provider

Check released functionality and lifecycle semantics upstream, not just an open
pull request or a matching name. Stop adding overlapping features once
`integrations/github` provides an equivalent, and plan the deprecation described in
the [compatibility contract](docs/guides/initial-contract.md) instead of
maintaining two implementations.
