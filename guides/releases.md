# Release validation

Releases remain manual `v*` tags. A merge or pull request never publishes a release.
The release workflow calls `ci.yml` from the same commit as the tag. Its publishing
job has `needs: validate`: a failed, cancelled or skipped validation prevents that
job from starting, including access to the production signing secrets.

The shared workflow runs Terraform and OpenTofu lifecycle tests, race detection,
vet, build, formatting, module and documentation checks, and signed snapshot
validation. `TF_REQUIRE_CLI=1` turns a missing integration CLI into a failure.
GoReleaser is pinned to the same version in validation and publication.

## Package checks

Run `make package-check` on Linux amd64 with Go, GoReleaser 2.18.3, GPG, Python 3,
Terraform 1.14.3 and OpenTofu 1.11.0 installed. The command:

1. Creates an isolated, disposable GPG signing key and passphrase.
2. Runs `goreleaser check` and builds signed snapshots with publication disabled.
3. Verifies six zip archives, binary names and executable bits, the protocol-6
   manifest, all seven SHA256 entries, and the detached signature against the
   expected public-key fingerprint in a separate keyring.
4. Installs the Linux amd64 package through a filesystem mirror under the actual
   `vmvarela/github-enterprise` address, using each CLI's default registry host.
5. Downloads the pinned official provider, validates the complete example and
   loads both provider schemas. These checks do not call the enterprise API.
6. Removes the disposable keyring and passphrase, including after failure.

The local mirror does not authenticate signatures itself; step 3 performs that
verification explicitly before installation. The two clean consumer directories
have no development overrides or inherited plugin caches. The build output stays
in the ignored `dist/` directory for inspection. No production signing key is
available to pull-request validation, including fork PRs.

## Publication prerequisites

Complete live acceptance (#4), review the [initial contract](initial-contract.md),
and prepare the Registry registration and signing credentials (#6) before tagging.
Configure `GPG_PRIVATE_KEY`, `GPG_PASSPHRASE` and `GPG_FINGERPRINT` as repository
secrets; never store their values in files tracked by git. Register the public key
with the registries. Prerelease tags such as `v0.1.0-rc.1` are marked as prereleases
by GoReleaser; stable tags such as `v0.1.0` are marked as stable releases.

The pipeline validation does not replace live acceptance or prove Registry
indexing. After publishing, verify installation from the actual registry, signature
verification, documentation rendering and coexistence with `integrations/github`.
