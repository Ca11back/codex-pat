from __future__ import annotations

import hashlib
from pathlib import Path
import tempfile
import unittest
import zipfile

from scripts import release


class ReleaseSetTest(unittest.TestCase):
    version = "0.1.5"

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
