from __future__ import annotations

import hashlib
from pathlib import Path
import tempfile
import unittest
import zipfile

from scripts import release


class ReleaseSetTest(unittest.TestCase):
    version = "0.1.8"

    def test_release_configuration_uses_current_cpa_host_smoke(self) -> None:
        root = Path(__file__).resolve().parents[1]
        makefile = (root / "Makefile").read_text(encoding="utf-8")
        workflow = (root / ".github/workflows/release.yml").read_text(encoding="utf-8")
        module = (root / "go.mod").read_text(encoding="utf-8")

        self.assertIn(f"VERSION ?= {self.version}", makefile)
        self.assertIn(f"default: {self.version}", workflow)
        self.assertIn(
            "github.com/router-for-me/CLIProxyAPI/v8 v8.1.2",
            module,
        )
        self.assertIn("CPA_VERSION: v8.1.2", workflow)
        self.assertEqual(workflow.count("CLIProxyAPI_8.1.2_"), 6)
        for runner, archive in (
            ("macos-15-intel", "darwin_amd64.tar.gz"),
            ("windows-11-arm", "windows_aarch64.zip"),
        ):
            self.assertIn(
                f"runner: {runner}\n            cpa_asset: CLIProxyAPI_8.1.2_{archive}",
                workflow,
            )
        self.assertNotIn("CPA_SCHEMA2", workflow)
        self.assertNotIn("CPA_SCHEMA3", workflow)
        self.assertNotIn("CLIProxyAPI_7.2.103_", workflow)
        self.assertNotIn("CLIProxyAPI_7.2.129_", workflow)
        self.assertIn("go run ./cmd/cpa-load-smoke", workflow)
        self.assertIn('-cpa "${CPA_BIN}"', workflow)

    def write_archive(self, directory: Path, goos: str, goarch: str) -> Path:
        path = directory / release.archive_name(self.version, goos, goarch)
        with zipfile.ZipFile(path, "w") as archive:
            archive.writestr(release.library_name(goos, goarch), b"native-library")
        return path

    def write_release_set(self, directory: Path) -> None:
        archives = []
        for goos, goarch, _ in release.TARGETS:
            archives.append(self.write_archive(directory, goos, goarch))
        checksums = "".join(
            f"{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.name}\n"
            for path in archives
        )
        (directory / "checksums.txt").write_text(checksums, encoding="utf-8")

    def test_supported_targets_are_exactly_six(self) -> None:
        self.assertEqual(
            release.TARGETS,
            (
                ("linux", "amd64", ".so"),
                ("linux", "arm64", ".so"),
                ("darwin", "amd64", ".dylib"),
                ("darwin", "arm64", ".dylib"),
                ("windows", "amd64", ".dll"),
                ("windows", "arm64", ".dll"),
            ),
        )
        with self.assertRaisesRegex(SystemExit, "unsupported release target: windows/386"):
            release.target("windows", "386")

    def test_verify_release_accepts_exact_six_asset_set(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            self.write_release_set(directory)
            release.verify_release(self.version, directory)

    def test_verify_release_requires_darwin_amd64_archive(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            self.write_release_set(directory)
            (directory / release.archive_name(self.version, "darwin", "amd64")).unlink()
            with self.assertRaisesRegex(SystemExit, r"missing release archive: .*darwin_amd64\.zip"):
                release.verify_release(self.version, directory)

    def test_verify_release_rejects_unsupported_archive(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            self.write_release_set(directory)
            unexpected = directory / f"codex-pat_{self.version}_windows_386.zip"
            with zipfile.ZipFile(unexpected, "w") as archive:
                archive.writestr("codex-pat.dll", b"unsupported-native-library")
            with self.assertRaisesRegex(SystemExit, "unexpected release archives"):
                release.verify_release(self.version, directory)


if __name__ == "__main__":
    unittest.main()
