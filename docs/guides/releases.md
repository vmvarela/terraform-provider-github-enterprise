---
page_title: "Release validation"
description: |-
  Packaging, signing and publication prerequisites for manual releases.
---

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

## Signing key

Release artefacts are signed with one dedicated GPG key, kept separate from commit
signing and from SSH keys.

- RSA 4096, passphrase protected, one year of validity from creation. Both registries
  reject expired keys, so renew before expiry; a published version is never replaced.
- User ID `Victor M. Varela (GitHub Enterprise provider release) <vmvarela@gmail.com>`.
  The key is `3610 065D B825 0D31 A49E  C0D5 2845 88A6 1CDA 90B9`, valid until
  2027-10-23. That fingerprint identifies it here and in the registry account settings.
- The private key, its passphrase and the revocation certificate stay with the
  maintainer: an encrypted export outside the repository plus an independent encrypted
  backup. No private material is committed, logged or pasted into an issue.
- Production values live in the repository secrets `GPG_PRIVATE_KEY`, `GPG_PASSPHRASE`
  and `GPG_FINGERPRINT`. The release job aborts while any of them is empty, and
  pull-request validation never receives them.
- Register the public key with each registry before publishing, and keep it in this
  directory as `signing-key.asc` so consumers can verify a release on their own.

Consumers verify a published signature without any private material:

```console
gpg --import signing-key.asc
gpg --verify terraform-provider-github-enterprise_0.1.0-rc.1_SHA256SUMS.sig \
  terraform-provider-github-enterprise_0.1.0-rc.1_SHA256SUMS
```

## Publication prerequisites

Complete live acceptance (#4), review the [initial contract](initial-contract.md),
and prepare the Registry registration and signing credentials (#6) before tagging.
Prerelease tags such as `v0.1.0-rc.1` are marked as prereleases
by GoReleaser; stable tags such as `v0.1.0` are marked as stable releases.

The pipeline validation does not replace live acceptance or prove Registry
indexing. After publishing, verify installation from the actual registry, signature
verification, documentation rendering and coexistence with `integrations/github`.
