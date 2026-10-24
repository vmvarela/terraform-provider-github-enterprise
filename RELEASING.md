# Releasing

Maintainer procedure for publishing a version. Nothing here is needed to install
the provider: consumers should read the [release verification guide](docs/guides/releases.md).

## Trigger and gates

A release is a manual `v*` tag pushed to the repository; merging a pull request
never publishes one. `.github/workflows/release.yml` calls `ci.yml` from the
tagged commit, and its publishing job declares `needs: validate`, so a failed,
cancelled or skipped validation run also blocks access to the production signing
secrets.

Validation covers `make check` (formatting, vet, race tests and Terraform
lifecycle tests), module tidiness, generated-documentation drift, `make build`,
`terraform fmt -check` on the examples, the OpenTofu lifecycle job, and
`make package-check` on Linux amd64. GoReleaser is pinned to the same version in
validation and publication. `TF_REQUIRE_CLI=1` turns a missing CLI into a
failure instead of a skip.

## Package checks

Run `make package-check` on Linux amd64 with Go, GoReleaser 2.18.3, GPG,
Python 3, Terraform 1.14.3 and OpenTofu 1.11.0 installed. The command:

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

Release artefacts are signed with one dedicated GPG key, kept separate from
commit signing and from SSH keys.

- RSA 4096, passphrase protected, one year of validity from creation. Both
  registries reject expired keys, so renew before expiry; a published version is
  never replaced.
- User ID `Victor M. Varela (GitHub Enterprise provider release) <vmvarela@gmail.com>`.
  The key is `3610 065D B825 0D31 A49E  C0D5 2845 88A6 1CDA 90B9`, valid until
  2027-10-23. That fingerprint identifies it in the registry account settings.
- The private key, its passphrase and the revocation certificate stay with the
  maintainer: an encrypted export outside the repository plus an independent
  encrypted backup. No private material is committed, logged or pasted into an
  issue.
- Production values live in the repository secrets `GPG_PRIVATE_KEY`,
  `GPG_PASSPHRASE` and `GPG_FINGERPRINT`. The release job aborts while any of them
  is empty.
- `docs/guides/signing-key.asc` carries the matching public key so consumers can
  verify a release; replace it together with the registry copy when the key is
  renewed. Keep the old public key available for versions signed with it.

## Before tagging

- [ ] Live acceptance has passed and its scope is recorded (see
      [CONTRIBUTING.md](CONTRIBUTING.md#live-acceptance-tests)).
- [ ] [The compatibility contract](docs/guides/initial-contract.md) still matches
      the shipped behaviour.
- [ ] `make check`, `make build` and `make docs` pass locally, and `make docs`
      leaves no diff.
- [ ] The signing key is unexpired, registered in both registry accounts, and its
      fingerprint matches `docs/guides/signing-key.asc`.
- [ ] `docs/` describes only consumer-facing behaviour.

## Tag and publish

```sh
git switch master
git pull
git tag v0.x.0
git push origin v0.x.0
```

GoReleaser marks `-rc` tags as prereleases and stable tags as stable releases.
Both registries ingest the GitHub release, so confirm the version appears in
Terraform and OpenTofu with all six platforms before announcing it. The Terraform
Registry only ingests releases signed by the key registered on the provider
account; the OpenTofu Registry requires the provider to be registered once through
its web form.

## After publishing

Verify the served documentation, installation from both registries, signature
verification following `docs/guides/releases.md`, and coexistence with
`integrations/github` through `examples/complete`. Registry pages are immutable
per version, so a documentation or packaging defect found afterwards is corrected
in the next version rather than in the published one.
