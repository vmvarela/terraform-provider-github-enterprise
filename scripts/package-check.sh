#!/usr/bin/env bash
# Build and verify signed snapshots without production credentials or publication.
set -euo pipefail
for tool in goreleaser terraform tofu gpg python3; do command -v "$tool" >/dev/null; done
scratch=$(mktemp -d)
export GNUPGHOME="$scratch/gnupg"
mkdir -m 700 "$GNUPGHOME"
cleanup() {
  gpgconf --kill all || true
  rm -rf "$scratch"
}
trap cleanup EXIT
export GPG_PASSPHRASE_FILE="$scratch/passphrase"
umask 077
python3 -c 'import secrets; print(secrets.token_hex(32))' > "$GPG_PASSPHRASE_FILE"
gpg --batch --pinentry-mode loopback --passphrase-file "$GPG_PASSPHRASE_FILE" \
  --quick-generate-key 'Provider CI <ci@example.invalid>' rsa2048 sign 1d
GPG_FINGERPRINT=$(gpg --batch --with-colons --list-secret-keys | awk -F: '$1 == "fpr" {print $10; exit}')
export GPG_FINGERPRINT
gpg --batch --armor --export "$GPG_FINGERPRINT" > "$scratch/public.asc"
goreleaser check
goreleaser release --snapshot --clean --skip=publish
python3 scripts/verify_release.py dist --public-key "$scratch/public.asc" --fingerprint "$GPG_FINGERPRINT"
python3 scripts/smoke_package.py dist
