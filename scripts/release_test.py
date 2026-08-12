from __future__ import annotations

import hashlib
from pathlib import Path
import tempfile
import unittest
import zipfile

from scripts import release


class ReleaseSetTest(unittest.TestCase):
    version = "0.1.6"

    def test_release_configuration_covers_verified_schema_two_and_three_hosts(self) -> None:
        root = Path(__file__).resolve().parents[1]
        makefile = (root / "Makefile").read_text(encoding="utf-8")
        workflow = (root / ".github/workflows/release.yml").read_text(encoding="utf-8")
        module = (root / "go.mod").read_text(encoding="utf-8")

        self.assertIn("VERSION ?= 0.1.6", makefile)
        self.assertIn(
            "github.com/router-for-me/CLIProxyAPI/v7 v7.2.129",
            module,
        )
        self.assertIn("CPA_SCHEMA2_VERSION: v7.2.103", workflow)
        self.assertIn("CPA_SCHEMA3_VERSION: v7.2.129", workflow)
        self.assertEqual(workflow.count("CLIProxyAPI_7.2.103_"), 4)
        self.assertEqual(workflow.count("CLIProxyAPI_7.2.129_"), 4)
        self.assertIn(
            'for cpa_bin in "${CPA_SCHEMA2_BIN}" "${CPA_SCHEMA3_BIN}"',
            workflow,
        )

    def write_archive(self, directory: Path, goos: str, goarch: str) -> Path:
        path = directory / release.archive_name(self.version, goos, goarch)
        with zipfile.ZipFile(path, "w") as archive:
            archive.writestr(release.library_name(goos, goarch), b"native-library")
        return path

    def write_four_asset_set(self, directory: Path) -> None:
        archives = []
        for goos, goarch, _ in release.TARGETS:
            archives.append(self.write_archive(directory, goos, goarch))
        checksums = "".join(
            f"{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.name}\n"
            for path in archives
        )
        (directory / "checksums.txt").write_text(checksums, encoding="utf-8")

    def test_supported_targets_are_exactly_four(self) -> None:
        self.assertEqual(
            release.TARGETS,
            (
                ("linux", "amd64", ".so"),
                ("linux", "arm64", ".so"),
                ("darwin", "arm64", ".dylib"),
                ("windows", "amd64", ".dll"),
            ),
        )
        with self.assertRaisesRegex(SystemExit, "unsupported release target: darwin/amd64"):
            release.target("darwin", "amd64")

    def test_verify_release_accepts_exact_four_asset_set(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            self.write_four_asset_set(directory)
            release.verify_release(self.version, directory)

    def test_verify_release_rejects_darwin_amd64_archive(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            self.write_four_asset_set(directory)
            unexpected = directory / f"codex-pat_{self.version}_darwin_amd64.zip"
            with zipfile.ZipFile(unexpected, "w") as archive:
                archive.writestr("codex-pat.dylib", b"unsupported-native-library")
            with self.assertRaisesRegex(SystemExit, "unexpected release archives"):
                release.verify_release(self.version, directory)


if __name__ == "__main__":
    unittest.main()
