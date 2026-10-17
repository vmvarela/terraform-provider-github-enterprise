# Contributing

Keep this provider focused on enterprise-level capabilities missing from `integrations/github`. Reproduce a lifecycle or API behavior issue before changing it.

Run `make check` and `make integration` before opening a pull request. HTTP-backed Terraform integration tests must not require real credentials. Add meaningful regression coverage for changes to identity, import, pagination, drift, and destructive operations. Use a dedicated enterprise for explicitly enabled real acceptance tests.

Use numeric team identities in relationships. Never trust a slug as immutable identity. Do not silently turn API permission failures into missing resources. Preserve recoverable state after partially applied assignment updates. Avoid automatic retries of non-idempotent operations.

Keep schemas, examples and Registry documentation consistent. Document ownership (individual or authoritative), import syntax, token permissions and compatibility limits. Do not add resources for reporting alone without a concrete Terraform use case.

Use conventional commit titles (`feat:`, `fix:`, `docs:`, `test:`, `ci:`). Releases are manual version tags; merging a pull request does not publish a release.
