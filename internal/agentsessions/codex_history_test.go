package agentsessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexResumeSandboxArgs(t *testing.T) {
	for mode, want := range map[string]string{"plan": "read-only", "dontAsk": "read-only", "default": "workspace-write", "bypassPermissions": "danger-full-access"} {
		for _, id := range []string{"", "thread"} {
			a := codexExecArgs("project", id, mode, "--prompt-not-an-option")
			if strings.Join(a[:5], " ") != "exec -C project -s "+want {
				t.Fatalf("sandbox missing: %q", a)
			}
			if a[len(a)-2] != "--" || a[len(a)-1] != "--prompt-not-an-option" {
				t.Fatal(a)
			}
			if id != "" && a[5] != "resume" {
				t.Fatal(a)
			}
		}
	}
}
func TestCodexIndexMetadataAliases(t *testing.T) {
	for _, key := range []string{"id", "session_id"} {
		root := t.TempDir()
		path := filepath.Join(root, "rollout-alias.jsonl")
		if err := os.WriteFile(path, []byte(`{"type":"session_meta","payload":{"`+key+`":"thread","cwd":"project"}}`+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		idx := &CodexIndex{Root: root}
		got, err := idx.Resolve("project", "thread")
		if err != nil || got != path {
			t.Fatalf("%s: %s %v", key, got, err)
		}
	}
}
