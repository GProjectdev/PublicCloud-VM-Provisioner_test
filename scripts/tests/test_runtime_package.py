import hashlib
import importlib.util
import json
from pathlib import Path
import shutil
import struct
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

REPO_ROOT = Path(__file__).resolve().parents[2]
SCRIPT_PATH = REPO_ROOT / "scripts" / "build-stateful-runtime-package.py"

spec = importlib.util.spec_from_file_location("runtime_package_builder", SCRIPT_PATH)
builder = importlib.util.module_from_spec(spec)
assert spec.loader is not None
spec.loader.exec_module(builder)


def write_elf(path: Path, machine: int, payload: bytes) -> None:
    header = bytearray(64)
    header[:4] = b"\x7fELF"
    header[4] = 2
    header[5] = 1
    header[6] = 1
    struct.pack_into("<H", header, 16, 3)
    struct.pack_into("<H", header, 18, machine)
    path.write_bytes(bytes(header) + payload)


def sha256(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


class RuntimePackageBuilderTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory(dir=REPO_ROOT)
        self.root = Path(self.tmp.name)
        self.inputs = self.root / "inputs"
        self.inputs.mkdir()
        self.output = self.root / "out"
        self.manifest = self.root / "manifest.json"

        self.crio = self.inputs / "crio"
        self.criu = self.inputs / "criu"
        self.cuda_checkpoint = self.inputs / "cuda-checkpoint"
        self.cuda_plugin = self.inputs / "cuda_plugin.so"
        write_elf(self.crio, 62, b"crio-reviewed")
        write_elf(self.criu, 62, b"criu-reviewed")
        write_elf(self.cuda_checkpoint, 62, b"checkpoint-reviewed")
        write_elf(self.cuda_plugin, 62, b"plugin-reviewed")

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def args(self) -> list[str]:
        return [
            "--crio",
            str(self.crio),
            "--criu",
            str(self.criu),
            "--cuda-checkpoint",
            str(self.cuda_checkpoint),
            "--cuda-plugin",
            str(self.cuda_plugin),
            "--output-dir",
            str(self.output),
            "--package-version",
            "1.0.0-1",
            "--arch",
            "amd64",
            "--k8s-minor",
            "1.35",
            "--crio-commit",
            "a" * 40,
            "--criu-commit",
            "b" * 40,
            "--adapter-sha256",
            "c" * 64,
            "--confirm-reviewed-adapter",
            "--dry-run",
            "--staged-manifest",
            str(self.manifest),
        ]

    def test_dry_run_writes_manifest_with_fixed_paths_and_hashes(self) -> None:
        self.assertEqual(builder.main(self.args()), 0)
        manifest = json.loads(self.manifest.read_text(encoding="utf-8"))

        self.assertEqual(manifest["formatVersion"], 1)
        self.assertEqual(manifest["kubernetesMinor"], "1.35")
        self.assertEqual(manifest["crioCommit"], "a" * 40)
        self.assertEqual(manifest["criuCommit"], "b" * 40)
        self.assertEqual(manifest["adapterSHA256"], "c" * 64)
        self.assertEqual(
            manifest["binaries"],
            {
                "/usr/local/bin/crio": sha256(self.crio),
                "/usr/local/sbin/criu": sha256(self.criu),
                "/usr/local/bin/cuda-checkpoint": sha256(self.cuda_checkpoint),
                "/usr/local/lib/criu/cuda_plugin.so": sha256(self.cuda_plugin),
            },
        )


    def test_dry_run_without_manifest_returns_success_message_only(self) -> None:
        args = self.args()
        manifest_index = args.index("--staged-manifest")
        del args[manifest_index : manifest_index + 2]
        self.assertEqual(builder.main(args), 0)
        self.assertFalse(self.manifest.exists())

    def test_requires_review_attestation(self) -> None:
        args = self.args()
        args.remove("--confirm-reviewed-adapter")
        self.assertEqual(builder.main(args), 2)

    def test_restore_profile_requires_clean_source_and_matching_binary(self) -> None:
        args = builder.parse_args(self.args() + ["--crio-source", str(self.root)])
        binary = self.root / "bin" / "crio"
        clean_version = "GitCommit: " + args.crio_commit + "\nGitTreeState: clean\n"
        cases = [
            (["", args.crio_commit, "", clean_version], False),
            ([" M server/container_restore.go"], True),
            (["", "f" * 40], True),
            (["", args.crio_commit, "", "GitCommit: " + "f" * 40], True),
            (["", args.crio_commit, "", clean_version.replace("clean", "dirty")], True),
        ]
        for output, rejected in cases:
            with self.subTest(output=output), mock.patch.object(builder.subprocess, "check_output", side_effect=output):
                if rejected:
                    with self.assertRaises(builder.BuildError):
                        builder.verify_restore_source(args, binary)
                else:
                    builder.verify_restore_source(args, binary)
        self.assertEqual(builder.build_manifest(args, {})["restoreProfile"], "gpu-file-v1")

    def test_restore_profile_rejects_wrong_binary_path(self) -> None:
        args = builder.parse_args(self.args() + ["--crio-source", str(self.root)])
        with mock.patch.object(builder.subprocess, "check_output", side_effect=["", args.crio_commit, ""]):
            with self.assertRaises(builder.BuildError):
                builder.verify_restore_source(args, self.crio)

    def test_rejects_symlink_input(self) -> None:
        if sys.platform.startswith("win"):
            self.skipTest("Windows symlink permissions vary under sandbox")
        link = self.inputs / "crio-link"
        try:
            link.symlink_to(self.crio)
        except (OSError, NotImplementedError) as exc:
            self.skipTest(f"symlinks unavailable: {exc}")
        args = self.args()
        args[args.index(str(self.crio))] = str(link)
        self.assertEqual(builder.main(args), 2)

    def test_rejects_arch_mismatch(self) -> None:
        write_elf(self.cuda_plugin, 183, b"arm-plugin")
        self.assertEqual(builder.main(self.args()), 2)

    def test_rejects_invalid_metadata(self) -> None:
        bad_cases = [
            ("--package-version", "../1"),
            ("--k8s-minor", "v1.35"),
            ("--crio-commit", "A" * 40),
            ("--adapter-sha256", "d" * 63),
            ("--depends", "cri-o\nBreaks: root"),
        ]
        for flag, value in bad_cases:
            with self.subTest(flag=flag):
                args = self.args()
                if flag == "--depends":
                    args.extend([flag, value])
                else:
                    args[args.index(flag) + 1] = value
                self.assertEqual(builder.main(args), 2)

    @unittest.skipUnless(sys.platform.startswith("linux") and shutil.which("dpkg-deb"), "dpkg-deb test is Linux-only")
    def test_builds_deb_when_dpkg_available(self) -> None:
        args = self.args()
        args.remove("--dry-run")
        manifest_index = args.index("--staged-manifest")
        del args[manifest_index : manifest_index + 2]

        self.assertEqual(builder.main(args), 0)
        deb_path = self.output / "stateful-migration-runtime_1.0.0-1_amd64.deb"
        self.assertTrue(deb_path.exists())

        extract_dir = self.root / "extract"
        subprocess.run(["dpkg-deb", "-x", str(deb_path), str(extract_dir)], check=True)
        manifest = json.loads((extract_dir / "usr/local/share/stateful-migration/runtime.json").read_text())
        self.assertEqual(manifest["binaries"]["/usr/local/bin/crio"], sha256(self.crio))


if __name__ == "__main__":
    unittest.main()
