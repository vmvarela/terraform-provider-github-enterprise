#!/usr/bin/env python3
"""Verify the signed release layout produced by the repository's GoReleaser config."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess
import tempfile
import zipfile

PROJECT = "terraform-provider-github-enterprise"
PLATFORMS = {(os, arch) for os in ("linux", "darwin", "windows") for arch in ("amd64", "arm64")}
ROOT = Path(__file__).resolve().parents[1]


def verify_layout(dist, manifest):
    version = json.loads((dist / "metadata.json").read_text())["version"]
    if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9.-]+)?", version):
        raise ValueError(f"Invalid release version: {version}")
    prefix = f"{PROJECT}_{version}"
    sums = dist / f"{prefix}_SHA256SUMS"
    entries = {}
    for line in sums.read_text().splitlines():
        digest, name = line.split()
        if name in entries or not re.fullmatch(r"[a-f0-9]{64}", digest):
            raise ValueError("Invalid or duplicate checksum entry")
        entries[name] = digest
    manifest_name = f"{prefix}_manifest.json"
    expected = {f"{prefix}_{os}_{arch}.zip" for os, arch in PLATFORMS} | {manifest_name}
    if set(entries) != expected:
        raise ValueError("Checksums must cover exactly six platform archives and the manifest")
    if {p.name for p in dist.glob("*.zip")} != expected - {manifest_name}:
        raise ValueError("Unexpected or missing platform archives")
    # GoReleaser uploads release.extra_files from the source path under this name.
    for name, digest in entries.items():
        file = manifest if name == manifest_name else dist / name
        if hashlib.sha256(file.read_bytes()).hexdigest() != digest:
            raise ValueError(f"Checksum mismatch: {name}")
    data = json.loads(manifest.read_text())
    if data.get("version") != 1 or data.get("metadata", {}).get("protocol_versions") != ["6.0"]:
        raise ValueError("The manifest must advertise protocol 6.0")
    for os, arch in PLATFORMS:
        with zipfile.ZipFile(dist / f"{prefix}_{os}_{arch}.zip") as archive:
            binary = f"{PROJECT}_v{version}" + (".exe" if os == "windows" else "")
            names = archive.namelist()
            if len(names) != len(set(names)) or set(names) != {binary, "LICENSE", "README.md"}:
                raise ValueError(f"Unexpected archive contents for {os}/{arch}: {names}")
            if archive.testzip() is not None or archive.getinfo(binary).file_size == 0:
                raise ValueError(f"Corrupt or empty binary for {os}/{arch}")
            if os != "windows" and not (archive.getinfo(binary).external_attr >> 16) & 0o111:
                raise ValueError(f"Binary is not executable for {os}/{arch}")
    return sums


def verify_signature(sums, public_key, fingerprint):
    if not re.fullmatch(r"[A-Fa-f0-9]{40}", fingerprint):
        raise ValueError("Expected a full GPG fingerprint")
    with tempfile.TemporaryDirectory(prefix="provider-verify-") as home:
        Path(home).chmod(0o700)
        cmd = ["gpg", "--homedir", home, "--batch"]
        subprocess.run(cmd + ["--import", str(public_key)], check=True, capture_output=True)
        result = subprocess.run(cmd + ["--status-fd", "1", "--verify", str(sums) + ".sig", str(sums)],
                                check=True, capture_output=True, text=True)
        fingerprints = {line.split()[2] for line in result.stdout.splitlines()
                        if line.startswith("[GNUPG:] VALIDSIG ")}
        if fingerprint.upper() not in fingerprints:
            raise ValueError("Signature was not made by the expected key")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("dist", type=Path)
    parser.add_argument("--public-key", type=Path, required=True)
    parser.add_argument("--fingerprint", required=True)
    args = parser.parse_args()
    sums = verify_layout(args.dist, ROOT / "terraform-registry-manifest.json")
    verify_signature(sums, args.public_key, args.fingerprint)
    print("Verified six archives, protocol-6 manifest, seven SHA256 checksums and detached signature.")


if __name__ == "__main__":
    main()
