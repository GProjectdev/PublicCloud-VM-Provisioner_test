#!/usr/bin/env python3
"""Build the reviewed Stateful Migration runtime .deb package."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import sys
import tempfile
from typing import Mapping

PACKAGE_NAME = "stateful-migration-runtime"
MANIFEST_PATH = "/usr/local/share/stateful-migration/runtime.json"
INSTALL_PATHS = {
    "crio": "/usr/local/bin/crio",
    "criu": "/usr/local/sbin/criu",
    "cudaCheckpoint": "/usr/local/bin/cuda-checkpoint",
    "cudaPlugin": "/usr/local/lib/criu/cuda_plugin.so",
}
BASE_DEPENDS = ("cri-o", "criu", "python3")
ARCH_MACHINES = {"amd64": 62, "arm64": 183}

HEX40_RE = re.compile(r"^[0-9a-f]{40}$")
HEX64_RE = re.compile(r"^[0-9a-f]{64}$")
DEB_VERSION_RE = re.compile(r"^[0-9][A-Za-z0-9.+~:-]*$")
K8S_MINOR_RE = re.compile(r"^[0-9]+\.[0-9]+$")
DEPENDS_RE = re.compile(
    r"^[A-Za-z0-9][A-Za-z0-9+.-]*(?::[A-Za-z0-9][A-Za-z0-9+.-]*)?"
    r"(?:\s*\((?:>=|<=|=|<<|>>)\s*[A-Za-z0-9][A-Za-z0-9.+:~\-]*\))?$"
)


class BuildError(ValueError):
    """Raised for caller-correctable package input errors."""


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def require_lower_hex(value: str, pattern: re.Pattern[str], field: str, length: int) -> str:
    if not pattern.fullmatch(value):
        raise BuildError(f"{field} must be lowercase hex with length {length}")
    return value


def validate_version(value: str) -> str:
    if not DEB_VERSION_RE.fullmatch(value) or ".." in value or "::" in value:
        raise BuildError("--package-version must be a simple Debian version string")
    return value


def validate_kubernetes_minor(value: str) -> str:
    if not K8S_MINOR_RE.fullmatch(value):
        raise BuildError("--k8s-minor must look like 1.35")
    return value


def validate_depends(value: str) -> str:
    entries = [entry.strip() for entry in value.split(",")]
    if not entries or any(not entry for entry in entries):
        raise BuildError("--depends must be a comma-separated dependency list")
    for entry in entries:
        if "\n" in entry or "\r" in entry or not DEPENDS_RE.fullmatch(entry):
            raise BuildError(f"invalid dependency entry: {entry!r}")
    return ", ".join(entries)


def require_regular_input(path: Path, flag: str) -> Path:
    try:
        st = path.lstat()
    except FileNotFoundError as exc:
        raise BuildError(f"{flag} does not exist: {path}") from exc
    if stat.S_ISLNK(st.st_mode):
        raise BuildError(f"{flag} must not be a symlink: {path}")
    if not stat.S_ISREG(st.st_mode):
        raise BuildError(f"{flag} must be a regular file: {path}")
    return path


def elf_machine(path: Path) -> int:
    with path.open("rb") as handle:
        header = handle.read(20)
    if len(header) < 20 or header[:4] != b"\x7fELF":
        raise BuildError(f"{path} is not an ELF binary")
    endian_marker = header[5]
    if endian_marker == 1:
        byteorder = "little"
    elif endian_marker == 2:
        byteorder = "big"
    else:
        raise BuildError(f"{path} has an invalid ELF byte order marker")
    return int.from_bytes(header[18:20], byteorder=byteorder)


def validate_elf_arch(paths: Mapping[str, Path], arch: str) -> None:
    expected = ARCH_MACHINES[arch]
    for name, path in paths.items():
        actual = elf_machine(path)
        if actual != expected:
            raise BuildError(f"{name} ELF machine {actual} does not match --arch {arch}")


def copy_with_mode(src: Path, dst: Path, mode: int) -> str:
    dst.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(src, dst, follow_symlinks=False)
    os.chmod(dst, mode)
    return sha256_file(dst)


def write_text_file(path: Path, content: str, mode: int) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(content, encoding="utf-8", newline="\n")
    os.chmod(path, mode)


def build_manifest(args: argparse.Namespace, binary_hashes: Mapping[str, str]) -> dict[str, object]:
    return {
        "formatVersion": 1,
        "kubernetesMinor": args.k8s_minor,
        "crioCommit": args.crio_commit,
        "criuCommit": args.criu_commit,
        "adapterSHA256": args.adapter_sha256,
        "binaries": dict(sorted(binary_hashes.items())),
    }


def stage_package(args: argparse.Namespace, staging: Path) -> dict[str, object]:
    input_paths = {
        "crio": require_regular_input(Path(args.crio), "--crio"),
        "criu": require_regular_input(Path(args.criu), "--criu"),
        "cudaCheckpoint": require_regular_input(Path(args.cuda_checkpoint), "--cuda-checkpoint"),
        "cudaPlugin": require_regular_input(Path(args.cuda_plugin), "--cuda-plugin"),
    }
    validate_elf_arch(input_paths, args.arch)

    binary_hashes = {}
    for name, src in input_paths.items():
        installed_path = INSTALL_PATHS[name]
        mode = 0o644 if name == "cudaPlugin" else 0o755
        binary_hashes[installed_path] = copy_with_mode(src, staging / installed_path.lstrip("/"), mode)

    manifest = build_manifest(args, binary_hashes)
    write_text_file(
        staging / MANIFEST_PATH.lstrip("/"),
        json.dumps(manifest, indent=2, sort_keys=True) + "\n",
        0o644,
    )

    control = "\n".join(
        [
            f"Package: {PACKAGE_NAME}",
            f"Version: {args.package_version}",
            "Section: admin",
            "Priority: optional",
            f"Architecture: {args.arch}",
            "Maintainer: Stateful Migration System <stateful-migration@example.invalid>",
            f"Depends: {args.depends}",
            "Description: Reviewed Stateful Migration runtime artifacts",
            " Files-only package for prebuilt CRI-O, CRIU, CUDA checkpoint helper,",
            " and CRIU CUDA plugin artifacts used by Stateful Migration workers.",
            "",
        ]
    )
    write_text_file(staging / "DEBIAN" / "control", control, 0o644)
    return manifest


def build_package(args: argparse.Namespace) -> Path | None:
    output_dir = Path(args.output_dir)
    output_dir.mkdir(parents=True, exist_ok=True)
    package_path = output_dir / f"{PACKAGE_NAME}_{args.package_version}_{args.arch}.deb"

    with tempfile.TemporaryDirectory(prefix="stateful-runtime-", dir=str(output_dir)) as tmp:
        staging = Path(tmp) / "root"
        staging.mkdir()
        manifest = stage_package(args, staging)
        if args.dry_run:
            if args.staged_manifest:
                manifest_path = Path(args.staged_manifest)
                manifest_path.parent.mkdir(parents=True, exist_ok=True)
                manifest_path.write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n", encoding="utf-8")
                return manifest_path
            return None

        subprocess.run(
            ["dpkg-deb", "--root-owner-group", "--build", str(staging), str(package_path)],
            check=True,
        )
    return package_path


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Build the files-only stateful-migration-runtime .deb from reviewed local artifacts.",
    )
    parser.add_argument("--crio", required=True, help="Reviewed CRI-O ELF binary")
    parser.add_argument("--criu", required=True, help="Reviewed CRIU ELF binary")
    parser.add_argument("--cuda-checkpoint", required=True, help="Reviewed cuda-checkpoint ELF helper")
    parser.add_argument("--cuda-plugin", required=True, help="Reviewed CRIU CUDA plugin ELF shared object")
    parser.add_argument("--output-dir", required=True, help="Directory for the resulting .deb")
    parser.add_argument("--package-version", required=True, help="Debian package version")
    parser.add_argument("--arch", required=True, choices=sorted(ARCH_MACHINES), help="Debian architecture")
    parser.add_argument("--k8s-minor", required=True, help="Kubernetes minor version, for example 1.35")
    parser.add_argument("--crio-commit", required=True, help="Lowercase 40-hex reviewed CRI-O commit")
    parser.add_argument("--criu-commit", required=True, help="Lowercase 40-hex reviewed CRIU commit")
    parser.add_argument("--adapter-sha256", required=True, help="Lowercase 64-hex reviewed adapter composite sha256")
    parser.add_argument(
        "--confirm-reviewed-adapter",
        action="store_true",
        help="Attest the adapter patch/helper composite was reviewed before packaging",
    )
    parser.add_argument(
        "--depends",
        default=", ".join(BASE_DEPENDS),
        help="Comma-separated Debian Depends line; default: cri-o, criu, python3",
    )
    parser.add_argument("--dry-run", action="store_true", help="Validate and stage without invoking dpkg-deb")
    parser.add_argument("--staged-manifest", help="With --dry-run, write the generated manifest to this path")
    args = parser.parse_args(argv)

    args.package_version = validate_version(args.package_version)
    args.k8s_minor = validate_kubernetes_minor(args.k8s_minor)
    args.crio_commit = require_lower_hex(args.crio_commit, HEX40_RE, "--crio-commit", 40)
    args.criu_commit = require_lower_hex(args.criu_commit, HEX40_RE, "--criu-commit", 40)
    args.adapter_sha256 = require_lower_hex(args.adapter_sha256, HEX64_RE, "--adapter-sha256", 64)
    args.depends = validate_depends(args.depends)
    if not args.confirm_reviewed_adapter:
        raise BuildError("--confirm-reviewed-adapter is required; this script packages but does not prove review")
    if args.staged_manifest and not args.dry_run:
        raise BuildError("--staged-manifest is only valid with --dry-run")
    return args


def main(argv: list[str] | None = None) -> int:
    try:
        args = parse_args(sys.argv[1:] if argv is None else argv)
        result = build_package(args)
    except (BuildError, subprocess.CalledProcessError, OSError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2

    if result is None:
        print("dry-run validation succeeded")
    else:
        print(result)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
