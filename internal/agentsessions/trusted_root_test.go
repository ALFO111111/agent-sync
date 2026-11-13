package agentsessions

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func testLink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" && os.IsPermission(err) {
			t.Skipf("symlink privilege unavailable: %v", err)
		}
		t.Fatal(err)
	}
}

func TestTrustedRootBoundary(t *testing.T) {
	// Do not canonicalize TempDir: on macOS /var itself is a system alias.
	parent := t.TempDir()
	root := filepath.Join(parent, "index")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "session")
	if err := os.WriteFile(path, []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := openTrustedRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	f, err := r.openPath(path)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	for _, p := range []string{filepath.Join(parent, "session"), filepath.Join(parent, "index-other", "session")} {
		if f, err := r.openPath(p); err == nil {
			f.Close()
			t.Fatalf("escaped root: %s", p)
		}
	}
	for _, rel := range []string{"../session", filepath.Join("..", "index-other", "session")} {
		if err := r.check(rel); err == nil {
			t.Fatalf("accepted %s", rel)
		}
	}
	for _, tc := range []struct{ name, target string }{{"inside", path}, {"outside", filepath.Join(parent, "outside")}} {
		t.Run(tc.name, func(t *testing.T) {
			testLink(t, tc.target, filepath.Join(root, tc.name))
			if f, err := r.open(tc.name); err == nil {
				f.Close()
				t.Fatal("accepted link")
			}
		})
	}
}

func TestIndexesTrustedAliasesAndLinks(t *testing.T) {
	for _, tool := range []string{"claude", "codex"} {
		t.Run(tool, func(t *testing.T) {
			parent := t.TempDir()
			root := filepath.Join(parent, "index")
			dir := root
			if tool == "claude" {
				dir = filepath.Join(root, "project")
			}
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			id := "11111111-1111-1111-1111-111111111111"
			name := id + ".jsonl"
			data := `{"cwd":"Project","type":"user","message":{"content":"hello"}}` + "\n"
			if tool == "codex" {
				name = "rollout-test.jsonl"
				data = `{"type":"session_meta","payload":{"id":"` + id + `","cwd":"Project"}}` + "\n"
			}
			path := filepath.Join(dir, name)
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			check := func(t *testing.T, indexRoot string) {
				t.Helper()
				var sessions []Session
				var path string
				var err error
				if tool == "claude" {
					idx := &Index{Root: indexRoot}
					sessions, err = idx.Sessions("Project")
					if err == nil {
						path, err = idx.Resolve("Project", id)
					}
				} else {
					idx := &CodexIndex{Root: indexRoot}
					sessions, err = idx.Sessions("Project")
					if err == nil {
						path, err = idx.Resolve("Project", id)
					}
				}
				if err != nil || len(sessions) != 1 || path == "" {
					t.Fatalf("sessions=%+v path=%s err=%v", sessions, path, err)
				}
				f, err := openIndexedSession(indexRoot, path)
				if err != nil {
					t.Fatal(err)
				}
				f.Close()
			}
			check(t, root)
			t.Run("root-alias", func(t *testing.T) {
				alias := filepath.Join(parent, "root-alias")
				testLink(t, root, alias)
				check(t, alias)
			})
			t.Run("parent-alias", func(t *testing.T) {
				alias := filepath.Join(t.TempDir(), "parent")
				testLink(t, parent, alias)
				check(t, filepath.Join(alias, "index"))
			})
			for _, outside := range []bool{false, true} {
				label := "inside"
				if outside {
					label = "outside"
				}
				t.Run("file-"+label, func(t *testing.T) {
					target := path
					if outside {
						target = filepath.Join(t.TempDir(), name)
						if err := os.WriteFile(target, []byte(data), 0600); err != nil {
							t.Fatal(err)
						}
					}
					linkName := "22222222-2222-2222-2222-222222222222.jsonl"
					if tool == "codex" {
						linkName = "rollout-link.jsonl"
					}
					link := filepath.Join(dir, linkName)
					testLink(t, target, link)
					defer os.Remove(link)
					check(t, root)
					if f, err := openIndexedSession(root, link); err == nil {
						f.Close()
						t.Fatal("opened linked file")
					}
				})
				t.Run("directory-"+label, func(t *testing.T) {
					target := dir
					if outside {
						target = t.TempDir()
						if err := os.WriteFile(filepath.Join(target, name), []byte(data), 0600); err != nil {
							t.Fatal(err)
						}
					}
					link := filepath.Join(root, "linked-directory")
					testLink(t, target, link)
					defer os.Remove(link)
					check(t, root)
					if f, err := openIndexedSession(root, filepath.Join(link, name)); err == nil {
						f.Close()
						t.Fatal("opened linked directory")
					}
				})
			}
			resolve := func(cwd, id string) error {
				if tool == "claude" {
					_, err := (&Index{Root: root}).Resolve(cwd, id)
					return err
				}
				_, err := (&CodexIndex{Root: root}).Resolve(cwd, id)
				return err
			}
			for _, bad := range []string{"../" + id, "..\\" + id, "missing"} {
				if resolve("Project", bad) == nil {
					t.Fatal("accepted bad id", bad)
				}
			}
			if resolve("other", id) == nil {
				t.Fatal("accepted wrong cwd")
			}
			err := resolve("project", id)
			if (err == nil) != (runtime.GOOS == "windows") {
				t.Fatalf("cwd case match err=%v", err)
			}
			duplicate := filepath.Join(dir, "rollout-duplicate.jsonl")
			if tool == "claude" {
				other := filepath.Join(root, "other")
				if err := os.Mkdir(other, 0700); err != nil {
					t.Fatal(err)
				}
				duplicate = filepath.Join(other, name)
			}
			if err := os.WriteFile(duplicate, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if resolve("Project", id) == nil {
				t.Fatal("accepted ambiguous id")
			}
		})
	}
}

func TestSameLocalPathCase(t *testing.T) {
	if !SameLocalPath(filepath.Join("Project", "child", ".."), "Project") {
		t.Fatal("clean path mismatch")
	}
	if SameLocalPath("Project", "project") != (runtime.GOOS == "windows") {
		t.Fatal("incorrect case rule")
	}
	if SameLocalPath(string(filepath.Separator), ".") {
		t.Fatal("root collapsed to current directory")
	}
}
