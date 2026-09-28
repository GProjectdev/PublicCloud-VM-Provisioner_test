package aws

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"dcn.ssu.ac.kr/infra/pkg/nodesoftware"
)

func TestRuntimeVerifierHeredocValidatesManifestAndBinaries(t *testing.T) {
	python := findPython(t)

	p := softwareParams()
	p.NodeSoftware = migrationSoftware()
	script, err := BuildStartupScript(p)
	if err != nil {
		t.Fatal(err)
	}
	verifier := extractRuntimeVerifier(t, script)

	files := map[string][]byte{
		nodesoftware.CRIOBinaryPath:     []byte("crio binary fixture\n"),
		nodesoftware.CRIUBinaryPath:     []byte("criu binary fixture\n"),
		nodesoftware.CUDACheckpointPath: []byte("cuda-checkpoint fixture\n"),
		nodesoftware.CUDAPluginPath:     []byte("cuda plugin fixture\n"),
	}
	baseManifest := map[string]any{
		"formatVersion":   float64(1),
		"kubernetesMinor": "1.35",
		"crioCommit":      strings.Repeat("b", 40),
		"criuCommit":      strings.Repeat("c", 40),
		"adapterSHA256":   strings.Repeat("d", 64),
		"binaries":        binaryHashes(files),
	}

	for _, path := range []string{nodesoftware.CRIOBinaryPath, nodesoftware.CRIUBinaryPath, nodesoftware.CUDACheckpointPath, nodesoftware.CUDAPluginPath} {
		if _, ok := baseManifest["binaries"].(map[string]string)[path]; !ok {
			t.Fatalf("base manifest missing binary key %s", path)
		}
	}

	for _, tc := range []struct {
		name      string
		mutate    func(map[string]any, map[string][]byte)
		crioVer   string
		wantErr   bool
		wantError string
	}{
		{name: "success matching1.35", crioVer: "crio version 1.35.2\nGitCommit: " + strings.Repeat("b", 40) + "\n"},
		{name: "binary provenance mismatch rejects", crioVer: "crio version 1.35.2\nGitCommit: " + strings.Repeat("f", 40) + "\n", wantErr: true, wantError: "binary GitCommit"},
		{name: "mismatch1.3 rejects", crioVer: "crio version 1.3.9\n", wantErr: true, wantError: "Kubernetes minor 1.3, want 1.35"},
		{name: "bad binary digest rejects", crioVer: "crio version 1.35.2\n", wantErr: true, wantError: "sha256 mismatch for /usr/local/bin/crio", mutate: func(manifest map[string]any, _ map[string][]byte) {
			manifest["binaries"].(map[string]string)[nodesoftware.CRIOBinaryPath] = strings.Repeat("0", 64)
		}},
		{name: "wrong source provenance rejects", crioVer: "crio version 1.35.2\n", wantErr: true, wantError: "runtime manifest crioCommit", mutate: func(manifest map[string]any, _ map[string][]byte) {
			manifest["crioCommit"] = strings.Repeat("e", 40)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest := cloneManifest(baseManifest)
			caseFiles := cloneFiles(files)
			if tc.mutate != nil {
				tc.mutate(manifest, caseFiles)
			}

			out, err := runRuntimeVerifier(python, verifier, verifierPayload{
				Manifest:    manifest,
				Files:       encodeFiles(caseFiles),
				CRIOVersion: tc.crioVer,
				CRIUVersion: "Version: 3.19\n",
			})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("verifier succeeded, want failure containing %q", tc.wantError)
				}
				if !strings.Contains(out, tc.wantError) {
					t.Fatalf("verifier failure = %q, want substring %q", out, tc.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("verifier failed: %v\n%s", err, out)
			}
		})
	}
}

type verifierPayload struct {
	Manifest    map[string]any    `json:"manifest"`
	Files       map[string]string `json:"files"`
	CRIOVersion string            `json:"crioVersion"`
	CRIUVersion string            `json:"criuVersion"`
}

func extractRuntimeVerifier(t *testing.T, script string) string {
	t.Helper()
	cmd := "python3 - "
	cmdStart := strings.Index(script, cmd)
	if cmdStart < 0 {
		t.Fatal("generated script missing python3 verifier command")
	}
	const heredocStart = "<<'PY'\n"
	heredocRel := strings.Index(script[cmdStart:], heredocStart)
	if heredocRel < 0 {
		t.Fatal("generated script missing verifier heredoc start")
	}
	start := cmdStart + heredocRel + len(heredocStart)
	endRel := strings.Index(script[start:], "\nPY\n")
	if endRel < 0 {
		t.Fatal("generated script missing verifier heredoc terminator")
	}
	return script[start : start+endRel]
}

func runRuntimeVerifier(python, verifier string, payload verifierPayload) (string, error) {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	harness := fmt.Sprintf(runtimeVerifierHarness,
		nodesoftware.RuntimeManifestPath,
		nodesoftware.CRIOBinaryPath,
		nodesoftware.CRIUBinaryPath,
		nodesoftware.CUDACheckpointPath,
		nodesoftware.CUDAPluginPath,
		nodesoftware.CRIOBinaryPath,
		nodesoftware.CRIUBinaryPath,
	)
	cmd := exec.Command(python, "-c", harness, string(payloadJSON))
	cmd.Stdin = strings.NewReader(verifier)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

const runtimeVerifierHarness = `
import io
import json
import os
import stat
import subprocess
import sys
from types import SimpleNamespace
from unittest import mock

payload = json.loads(sys.argv[1])
verifier = sys.stdin.read()
manifest_path = %q
files = {path: bytes.fromhex(data) for path, data in payload["files"].items()}
manifest_json = json.dumps(payload["manifest"])

def fake_open(path, mode="r", *args, **kwargs):
    if path == manifest_path:
        if "b" in mode:
            return io.BytesIO(manifest_json.encode("utf-8"))
        return io.StringIO(manifest_json)
    if path in files:
        if "b" not in mode:
            raise AssertionError(f"binary path {path} opened without binary mode")
        return io.BytesIO(files[path])
    raise FileNotFoundError(path)

def fake_lstat(path):
    if path not in files:
        raise FileNotFoundError(path)
    return SimpleNamespace(st_mode=stat.S_IFREG | 0o755)

def fake_access(path, mode):
    if path == %q:
        return True
    if path == %q:
        return True
    if path == %q:
        return True
    if path == %q:
        return False
    return False

def fake_check_output(argv, **kwargs):
    if argv == [%q, "--version"]:
        return payload["crioVersion"]
    if argv == [%q, "--version"]:
        return payload["criuVersion"]
    raise AssertionError(f"unexpected check_output argv {argv!r}")

with mock.patch("builtins.open", side_effect=fake_open), \
     mock.patch("os.lstat", side_effect=fake_lstat), \
     mock.patch("os.access", side_effect=fake_access), \
     mock.patch("subprocess.check_output", side_effect=fake_check_output), \
     mock.patch.object(sys, "argv", ["-", manifest_path]):
    exec(compile(verifier, "<runtime-verifier>", "exec"), {"__name__": "__main__"})
`

func findPython(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"python3", "python"} {
		path, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		if out, err := exec.Command(path, "--version").CombinedOutput(); err == nil && strings.HasPrefix(string(out), "Python ") {
			return path
		}
	}
	t.Skip("python3/python unavailable")
	return ""
}

func binaryHashes(files map[string][]byte) map[string]string {
	hashes := make(map[string]string, len(files))
	for path, data := range files {
		sum := sha256.Sum256(data)
		hashes[path] = hex.EncodeToString(sum[:])
	}
	return hashes
}

func encodeFiles(files map[string][]byte) map[string]string {
	encoded := make(map[string]string, len(files))
	for path, data := range files {
		encoded[path] = hex.EncodeToString(data)
	}
	return encoded
}

func cloneManifest(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		if binaries, ok := value.(map[string]string); ok {
			out[key] = cloneStringMap(binaries)
			continue
		}
		out[key] = value
	}
	return out
}

func cloneFiles(in map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, len(in))
	for key, value := range in {
		out[key] = append([]byte(nil), value...)
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
