#!/usr/bin/env python3
"""Build-artifact validation and deterministic release packaging."""

from __future__ import annotations

import argparse
import hashlib
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import zipfile


PLUGIN_ID = "codex-pat"
VERSION_RE = re.compile(r"^[0-9]+\.[0-9]+\.[0-9]+$")
EXPORTS = (
    "cliproxy_plugin_init",
    "cliproxyPluginCall",
    "cliproxyPluginFree",
    "cliproxyPluginShutdown",
)
TARGETS = (
    ("linux", "amd64", ".so"),
    ("linux", "arm64", ".so"),
    ("darwin", "arm64", ".dylib"),
    ("windows", "amd64", ".dll"),
)


def fail(message: str) -> None:
    raise SystemExit(message)


def validate_version(version: str) -> str:
    if not VERSION_RE.fullmatch(version):
        fail(f"version must be dotted numeric x.y.z, got {version!r}")
    return version


def target(goos: str, goarch: str) -> tuple[str, str, str]:
    for item in TARGETS:
        if item[:2] == (goos, goarch):
            return item
    fail(f"unsupported release target: {goos}/{goarch}")


def archive_name(version: str, goos: str, goarch: str) -> str:
    target(goos, goarch)
    return f"{PLUGIN_ID}_{version}_{goos}_{goarch}.zip"


def library_name(goos: str, goarch: str) -> str:
    return PLUGIN_ID + target(goos, goarch)[2]


def run_output(command: list[str]) -> str:
    executable = shutil.which(command[0])
    if executable is None:
        fail(f"required inspection tool is unavailable: {command[0]}")
    completed = subprocess.run(
        [executable, *command[1:]],
        check=False,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
    )
    if completed.returncode != 0:
        fail(f"{' '.join(command)} failed:\n{completed.stdout}")
    return completed.stdout


def verify_library(path: Path, goos: str, goarch: str, glibc_max: str | None) -> None:
    target(goos, goarch)
    if not path.is_file() or path.stat().st_size == 0:
        fail(f"library is missing or empty: {path}")

    description = run_output(["file", str(path)])
    description_lower = description.lower()
    os_tokens = {
        "linux": ("elf",),
        "darwin": ("mach-o",),
        "windows": ("pe32+", "dll"),
    }[goos]
    if not all(token in description_lower for token in os_tokens):
        fail(f"unexpected {goos} library format: {description.strip()}")
    arch_tokens = {
        "amd64": ("x86-64", "x86_64", "amd64"),
        "arm64": ("aarch64", "arm64"),
    }[goarch]
    if not any(token in description_lower for token in arch_tokens):
        fail(f"unexpected {goarch} library architecture: {description.strip()}")

    if goos == "linux":
        symbols = run_output(["nm", "-D", "--defined-only", str(path)])
    elif goos == "darwin":
        symbols = run_output(["nm", "-gU", str(path)])
    else:
        symbols = run_output(["objdump", "-p", str(path)])
    missing = [symbol for symbol in EXPORTS if symbol not in symbols]
    if missing:
        fail(f"missing required plugin exports in {path}: {', '.join(missing)}")

    if glibc_max is not None:
        if goos != "linux":
            fail("--glibc-max is valid only for Linux")
        maximum = tuple(int(part) for part in glibc_max.split("."))
        version_info = run_output(["readelf", "--version-info", str(path)])
        required = {
            tuple(int(part) for part in match.split("."))
            for match in re.findall(r"GLIBC_([0-9]+(?:\.[0-9]+)+)", version_info)
        }
        too_new = sorted(item for item in required if item > maximum)
        if too_new:
            rendered = ", ".join(".".join(map(str, item)) for item in too_new)
            fail(f"{path} requires GLIBC newer than {glibc_max}: {rendered}")


def package(version: str, goos: str, goarch: str, library: Path, output: Path) -> None:
    validate_version(version)
    root_name = library_name(goos, goarch)
    if library.suffix.lower() != Path(root_name).suffix:
        fail(f"wrong library extension for {goos}/{goarch}: {library.name}")
    if not library.is_file() or library.stat().st_size == 0:
        fail(f"library is missing or empty: {library}")
    output.mkdir(parents=True, exist_ok=True)
    destination = output / archive_name(version, goos, goarch)
    info = zipfile.ZipInfo(root_name, date_time=(1980, 1, 1, 0, 0, 0))
    info.compress_type = zipfile.ZIP_DEFLATED
    info.external_attr = (0o755 & 0xFFFF) << 16
    with zipfile.ZipFile(destination, "w") as archive:
        archive.writestr(info, library.read_bytes())
    verify_archive(destination, root_name)
    print(destination)


def verify_archive(path: Path, expected_library: str) -> None:
    try:
        with zipfile.ZipFile(path) as archive:
            entries = archive.infolist()
            if len(entries) != 1 or entries[0].filename != expected_library:
                names = [item.filename for item in entries]
                fail(f"{path} must contain only root {expected_library}, got {names}")
            if entries[0].is_dir() or entries[0].file_size == 0:
                fail(f"{path} contains an empty library")
            with archive.open(entries[0]) as library:
                while library.read(1024 * 1024):
                    pass
    except zipfile.BadZipFile as error:
        fail(f"invalid zip {path}: {error}")


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def write_checksums(version: str, directory: Path) -> None:
    validate_version(version)
    expected = []
    for goos, goarch, _ in TARGETS:
        path = directory / archive_name(version, goos, goarch)
        verify_archive(path, library_name(goos, goarch))
        expected.append(path)
    unexpected = sorted(path.name for path in directory.glob("*.zip") if path not in expected)
    if unexpected:
        fail(f"unexpected release archives: {', '.join(unexpected)}")
    contents = "".join(f"{sha256(path)}  {path.name}\n" for path in expected)
    (directory / "checksums.txt").write_text(contents, encoding="utf-8", newline="\n")


def verify_release(version: str, directory: Path) -> None:
    validate_version(version)
    expected_names = []
    for goos, goarch, _ in TARGETS:
        name = archive_name(version, goos, goarch)
        verify_archive(directory / name, library_name(goos, goarch))
        expected_names.append(name)
    unexpected = sorted(
        path.name for path in directory.glob("*.zip") if path.name not in expected_names
    )
    if unexpected:
        fail(f"unexpected release archives: {', '.join(unexpected)}")

    checksum_path = directory / "checksums.txt"
    if not checksum_path.is_file():
        fail(f"missing {checksum_path}")
    lines = [line for line in checksum_path.read_text(encoding="utf-8").splitlines() if line]
    parsed: dict[str, str] = {}
    for line in lines:
        match = re.fullmatch(r"([0-9a-f]{64})  ([^/\\]+)", line)
        if match is None or match.group(2) in parsed:
            fail(f"invalid sha256sum line: {line!r}")
        parsed[match.group(2)] = match.group(1)
    if set(parsed) != set(expected_names):
        fail(f"checksums.txt must cover exactly {expected_names}, got {sorted(parsed)}")
    for name in expected_names:
        if sha256(directory / name) != parsed[name]:
            fail(f"checksum mismatch: {name}")
    print(f"verified four archives and checksums in {directory}")


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(description=__doc__)
    subparsers = result.add_subparsers(dest="command", required=True)

    version_parser = subparsers.add_parser("validate-version")
    version_parser.add_argument("version")

    target_parser = subparsers.add_parser("validate-target")
    target_parser.add_argument("--goos", required=True)
    target_parser.add_argument("--goarch", required=True)

    library_parser = subparsers.add_parser("verify-library")
    library_parser.add_argument("--library", type=Path, required=True)
    library_parser.add_argument("--goos", required=True)
    library_parser.add_argument("--goarch", required=True)
    library_parser.add_argument("--glibc-max")

    package_parser = subparsers.add_parser("package")
    package_parser.add_argument("--version", required=True)
    package_parser.add_argument("--goos", required=True)
    package_parser.add_argument("--goarch", required=True)
    package_parser.add_argument("--library", type=Path, required=True)
    package_parser.add_argument("--output", type=Path, required=True)

    checksums_parser = subparsers.add_parser("checksums")
    checksums_parser.add_argument("--version", required=True)
    checksums_parser.add_argument("--directory", type=Path, required=True)

    verify_parser = subparsers.add_parser("verify-release")
    verify_parser.add_argument("--version", required=True)
    verify_parser.add_argument("--directory", type=Path, required=True)
    return result


def main() -> None:
    arguments = parser().parse_args()
    if arguments.command == "validate-version":
        print(validate_version(arguments.version))
    elif arguments.command == "validate-target":
        print("/".join(target(arguments.goos, arguments.goarch)[:2]))
    elif arguments.command == "verify-library":
        verify_library(
            arguments.library,
            arguments.goos,
            arguments.goarch,
            arguments.glibc_max,
        )
    elif arguments.command == "package":
        package(
            arguments.version,
            arguments.goos,
            arguments.goarch,
            arguments.library,
            arguments.output,
        )
    elif arguments.command == "checksums":
        write_checksums(arguments.version, arguments.directory)
    elif arguments.command == "verify-release":
        verify_release(arguments.version, arguments.directory)


if __name__ == "__main__":
    main()
