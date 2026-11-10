package v1

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"ergo.local/prototype/runtime/rt"
)

// TestFSRootKeepsOpenedDirectory is the smallest repro of the root-name
// swap bug: after the opened directory is renamed and replaced, every
// operation must still see the directory that OpenRoot opened. The tree
// agreement property checks paths inside a root, not the root's identity.
func TestFSRootKeepsOpenedDirectory(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "root")
	if err := os.Mkdir(name, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(name, "original.txt"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	task := rt.Root("root identity")
	s := NewScope(task)
	defer s.End(nil)
	root, err := FSOpenRoot(&s, name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(name, filepath.Join(dir, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(name, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(name, "replacement.txt"), []byte("replacement"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := root.ReadFile(task, "original.txt"); err != nil || got != "original" {
		t.Errorf("ReadFile after root replacement = %q, %v; want original", got, err)
	}
	if got, err := root.ReadDir(task, "."); err != nil || !reflect.DeepEqual(got, []string{"original.txt"}) {
		t.Errorf("ReadDir after root replacement = %v, %v; want original.txt", got, err)
	}
	if got, err := root.Stat(task, "original.txt"); err != nil || got.Name != "original.txt" || got.Size != len("original") {
		t.Errorf("Stat after root replacement = %+v, %v; want original.txt", got, err)
	}
}

// TestFSRootClosesAtScopeEnd kills an omitted release: a retained Root
// cannot still read after the acquiring frame ends, on any exit. This
// uses the host operations themselves, not descriptor-count inspection.
func TestFSRootClosesAtScopeEnd(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, exit := range []string{"normal", "failure", "defect", "stop"} {
		t.Run(exit, func(t *testing.T) {
			task := rt.Root("root release")
			var root *HostRoot
			func() {
				defer func() { _ = recover() }()
				_ = frame(task, func(s *Scope) error {
					var err error
					root, err = FSOpenRoot(s, dir)
					if err != nil {
						t.Fatal(err)
					}
					if text, err := root.ReadFile(task, "file"); text != "content" || err != nil {
						t.Fatalf("open Root = %q, %v", text, err)
					}
					switch exit {
					case "failure":
						return bad{"root release"}
					case "defect":
						panic("root release")
					case "stop":
						task.Interrupt()
						task.Check()
					}
					return nil
				})
			}()
			if root == nil {
				t.Fatal("Root was not acquired")
			}
			if _, err := root.ReadFile(task, "file"); err == nil {
				t.Error("ReadFile succeeded after scope end")
			}
			if _, err := root.ReadDir(task, "."); err == nil {
				t.Error("ReadDir succeeded after scope end")
			}
			if _, err := root.Stat(task, "file"); err == nil {
				t.Error("Stat succeeded after scope end")
			}
		})
	}
}
