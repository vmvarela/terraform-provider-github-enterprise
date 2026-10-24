---
page_title: "Release verification"
description: |-
  How to verify a released provider package, its signature and its supported versions.
---

# Release verification

Every release publishes signed packages for Terraform and OpenTofu. This page
explains what a release contains and how to verify a download before installing
it. Maintainer procedures are out of scope; see
[RELEASING.md](https://github.com/vmvarela/terraform-provider-github-enterprise/blob/master/RELEASING.md).

## What a release contains

Each `v*` release on
[GitHub](https://github.com/vmvarela/terraform-provider-github-enterprise/releases)
provides:

- one zip archive per platform: `linux`, `darwin` and `windows`, each for `amd64`
  and `arm64`;
- `<version>_SHA256SUMS` with the checksums of every archive and of the manifest;
- `<version>_SHA256SUMS.sig`, the detached GPG signature of the checksum file;
- `<version>_manifest.json`, the protocol-6 manifest read by Terraform.

The provider implements protocol 6, supported by current Terraform and OpenTofu
releases. It is validated with Terraform 1.14.3 and OpenTofu 1.11.0; other CLI
versions have not been tested.

## Verify a download

Set `VERSION` to the release you pinned, without the leading `v`, and download the
archive, checksum file and signature from that release:

```console
VERSION=0.1.0
BASE="https://github.com/vmvarela/terraform-provider-github-enterprise/releases/download/v$VERSION"
curl -LO "$BASE/terraform-provider-github-enterprise_${VERSION}_SHA256SUMS"
curl -LO "$BASE/terraform-provider-github-enterprise_${VERSION}_SHA256SUMS.sig"
curl -LO "$BASE/terraform-provider-github-enterprise_${VERSION}_linux_amd64.zip"
```

Import the public key kept in this documentation set, then check the signature and
the archive:

```console
curl -LO "https://raw.githubusercontent.com/vmvarela/terraform-provider-github-enterprise/v$VERSION/docs/guides/signing-key.asc"
gpg --import signing-key.asc
gpg --verify terraform-provider-github-enterprise_${VERSION}_SHA256SUMS.sig \
  terraform-provider-github-enterprise_${VERSION}_SHA256SUMS
sha256sum --check --ignore-missing terraform-provider-github-enterprise_${VERSION}_SHA256SUMS
```

The signature must report a good signature from `Victor M. Varela (GitHub
Enterprise provider release) <vmvarela@gmail.com>` with the fingerprint below.
Stop if it reports a different key or a bad signature, and report the release.
On macOS, `shasum -a 256 --check` replaces `sha256sum`.

## Signing key

| Property | Value |
| --- | --- |
| Fingerprint | `3610 065D B825 0D31 A49E  C0D5 2845 88A6 1CDA 90B9` |
| User ID | `Victor M. Varela (GitHub Enterprise provider release) <vmvarela@gmail.com>` |
| Algorithm | RSA 4096, passphrase protected |
| Expires | 2027-10-23 |

A later release may use a renewed key. Each published version keeps the fingerprint
that signed it: check the one printed with the version you downloaded instead of
assuming this one.

## Versioning and immutability

Versions follow `vMAJOR.MINOR.PATCH`, starting at `v0.1.0`. Within a minor
version the provider preserves resource names, attribute meanings, defaults,
ownership and import formats, as described in the
[compatibility contract](https://github.com/vmvarela/terraform-provider-github-enterprise/blob/master/docs/guides/initial-contract.md).

Published versions are immutable. Registry pages are rendered from the tagged
commit, so a documentation or packaging correction ships in a later version and
never replaces a published one. Prerelease tags such as `v0.1.0-rc.1` are marked
as prereleases, are not offered as the latest version, and can be withdrawn before
the stable release; do not pin them.
