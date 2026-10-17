"""Regression tests for release layout validation, without external tools."""
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
import zipfile

from verify_release import PLATFORMS, PROJECT, verify_layout


class LayoutTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.dist = Path(self.temp.name)
        self.prefix = f"{PROJECT}_0.1.0-test"
        self.manifest = self.dist / "manifest.json"
        self.manifest.write_text(json.dumps({"version": 1, "metadata": {"protocol_versions": ["6.0"]}}))
        (self.dist / "metadata.json").write_text('{"version":"0.1.0-test"}')
        for os, arch in PLATFORMS:
            self.archive(os, arch)
        self.checksums()

    def archive(self, os, arch, executable=True, wrong_name=False):
        name = "wrong" if wrong_name else f"{PROJECT}_v0.1.0-test"
        if os == "windows":
            name += ".exe"
        with zipfile.ZipFile(self.dist / f"{self.prefix}_{os}_{arch}.zip", "w") as archive:
            binary = zipfile.ZipInfo(name)
            binary.external_attr = (0o100755 if executable else 0o100644) << 16
            archive.writestr(binary, b"binary fixture")
            archive.writestr("LICENSE", "license")
            archive.writestr("README.md", "readme")

    def checksums(self):
        entries = {p.name: p for p in self.dist.glob("*.zip")}
        entries[f"{self.prefix}_manifest.json"] = self.manifest
        self.sums = self.dist / f"{self.prefix}_SHA256SUMS"
        self.sums.write_text("".join(f"{hashlib.sha256(p.read_bytes()).hexdigest()}  {name}\n" for name, p in entries.items()))

    def test_valid_layout(self):
        self.assertEqual(verify_layout(self.dist, self.manifest), self.sums)

    def test_corrupt_archive(self):
        next(self.dist.glob("*.zip")).write_bytes(b"corrupted")
        with self.assertRaisesRegex(ValueError, "Checksum mismatch"):
            verify_layout(self.dist, self.manifest)

    def test_missing_archive(self):
        next(self.dist.glob("*.zip")).unlink()
        with self.assertRaisesRegex(ValueError, "missing platform"):
            verify_layout(self.dist, self.manifest)

    def test_duplicate_checksum(self):
        self.sums.write_text(self.sums.read_text() * 2)
        with self.assertRaisesRegex(ValueError, "duplicate"):
            verify_layout(self.dist, self.manifest)

    def test_wrong_protocol(self):
        self.manifest.write_text('{"version":1,"metadata":{"protocol_versions":["5.0"]}}')
        self.checksums()
        with self.assertRaisesRegex(ValueError, "protocol 6.0"):
            verify_layout(self.dist, self.manifest)

    def test_wrong_binary_name(self):
        self.archive("linux", "amd64", wrong_name=True)
        self.checksums()
        with self.assertRaisesRegex(ValueError, "archive contents"):
            verify_layout(self.dist, self.manifest)

    def test_non_executable_binary(self):
        self.archive("linux", "amd64", executable=False)
        self.checksums()
        with self.assertRaisesRegex(ValueError, "not executable"):
            verify_layout(self.dist, self.manifest)


if __name__ == "__main__":
    unittest.main()
