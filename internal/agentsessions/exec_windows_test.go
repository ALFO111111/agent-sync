//go:build windows

package agentsessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResumeIDRejectsShellMeta(t *testing.T) {
	bad := []string{
		`abc&calc`,
		`abc;rm -rf`,
		`abc|whoami`,
		`--help`,
		`a b c d e`,
		`$(whoami)`,
		`abc"def"g`,
		"abc\ndefg",
		`short`,
	}
	for _, id := range bad {
		if validResumeID(id) {
			t.Fatalf("resume id %q should be rejected", id)
		}
	}
	for _, id := range []string{"", "0b8f1e2a-1234-4c5d-9e8f-0123456789ab", "session_1234"} {
		if !validResumeID(id) {
			t.Fatalf("resume id %q should be accepted", id)
		}
	}
}

func TestResolveCmdShim(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "node_modules", "pkg", "cli.js")
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("// fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	node := filepath.Join(dir, "node.exe")
	if err := os.WriteFile(node, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(dir, "fake.cmd")
	body := "@ECHO off\r\nGOTO start\r\n:find_dp0\r\nSET dp0=%~dp0\r\n:start\r\nSETLOCAL\r\n\"%dp0%\\node.exe\"  \"%dp0%\\node_modules\\pkg\\cli.js\" %*\r\n"
	if err := os.WriteFile(shim, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	gotNode, gotScript, err := resolveCmdShim(shim)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(gotNode, node) {
		t.Fatalf("node = %q want %q", gotNode, node)
	}
	if !strings.EqualFold(gotScript, script) {
		t.Fatalf("script = %q want %q", gotScript, script)
	}
	cmd, err := agentCommand(shim, []string{"-p", "hello & calc"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(cmd.Path), "cmd.exe") {
		t.Fatalf("command must not go through cmd.exe: %q", cmd.Path)
	}
	if len(cmd.Args) < 4 || cmd.Args[1] != script {
		t.Fatalf("args = %#v", cmd.Args)
	}
}

func TestResolveCmdShimExeTarget(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "pkg", "tool.exe")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(dir, "tool.cmd")
	body := "@ECHO off\r\n\"%dp0%\\pkg\\tool.exe\" %*\r\n"
	if err := os.WriteFile(shim, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, script, err := resolveCmdShim(shim)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(got, exe) || script != "" {
		t.Fatalf("resolved %q/%q", got, script)
	}
	cmd, err := agentCommand(shim, []string{"hello & calc"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(cmd.Path, exe) || strings.Contains(strings.ToLower(cmd.Path), "cmd.exe") {
		t.Fatalf("command = %q", cmd.Path)
	}
}

func TestResolveCmdShimRejectsUnknown(t *testing.T) {
	shim := filepath.Join(t.TempDir(), "bad.cmd")
	if err := os.WriteFile(shim, []byte("@echo off\r\necho hi\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveCmdShim(shim); err == nil {
		t.Fatal("expected an error for a .cmd without a node script")
	}
}
