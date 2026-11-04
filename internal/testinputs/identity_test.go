package testinputs

import (
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
)

// seenWatcher remembers each lookup a walk announced.
type seenWatcher struct {
	mu   sync.Mutex
	seen map[string]bool
}

func (w *seenWatcher) see(dir, name string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seen[filepath.Join(dir, name)] = true
}

func (w *seenWatcher) finish() ([]string, string) { return nil, "" }

// TestIdentityModeRecordsTheSameLines: in identity mode the recorder keeps
// the same log lines as without it (the walk decides what reaches the root,
// as recorder.walk does), keeps a walk for the path as named and as logged,
// and announces every lookup of a walk to the watcher before making it.
func TestIdentityModeRecordsTheSameLines(t *testing.T) {
	logical, physical, outside := linkedModule(t)
	for _, name := range []string{"src/e.ts", "src/a.ts", "src/sub/f.ts"} {
		p := filepath.Join(physical, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{
		filepath.Join(outside, "e.ts"):          filepath.Join(physical, "src/e.ts"),
		filepath.Join(outside, "h.ts"):          filepath.Join(physical, "gone/h.ts"),
		filepath.Join(physical, "dirs/current"): "../src",
		filepath.Join(outside, "loop"):          "loop",
	} {
		os.MkdirAll(filepath.Dir(link), 0o755)
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	paths := []string{
		filepath.Join(logical, "src/a.ts"), filepath.Join(physical, "src/a.ts"),
		filepath.Join(outside, "src/new.ts"), filepath.Join(outside, "src/no/dir/x.ts"),
		filepath.Join(outside, "missing/x.ts"), logical + "/src//./c.ts", logical + "/src/../src/a.ts",
		"/proc/self/status", "/dev/null", "/usr/lib/libc.so.6", logical,
		filepath.Join(outside, "e.ts"), outside + "/src/../g.ts", filepath.Join(outside, "h.ts"),
		filepath.Join(logical, "dirs/current/sub/f.ts"), filepath.Join(outside, "loop/x"),
		"/dev/../" + logical + "/src/x", outside + "/src/sub/../../bin",
	}
	plain := newRecorder(logical)
	ident := newRecorder(logical)
	w := &seenWatcher{seen: map[string]bool{}}
	ident.ident = newIdentityRecord(filepath.Join(t.TempDir(), "identity.json"), w)
	for i, p := range paths {
		for _, r := range []*recorder{plain, ident} {
			if i%2 == 0 {
				r.open(p, true)
			} else {
				r.stat(p)
			}
		}
	}
	plain.stop()
	ident.stop()
	ident.finishIdentity()
	if got, want := ident.lines(), plain.lines(); !slices.Equal(got, want) {
		t.Fatalf("identity mode records other lines:\n got %q\nwant %q", got, want)
	}
	if ident.reason() != "" {
		t.Fatalf("refused: %s", ident.reason())
	}
	id, err := ReadIdentity(ident.ident.file)
	if err != nil {
		t.Fatal(err)
	}
	for path := range ident.paths {
		if len(id.Walks[path]) == 0 {
			t.Errorf("the logged path %s has no walk", path)
		}
	}
	for _, p := range paths {
		if ident.identify(clean(p)).entered && len(id.Walks[IdentityKey(p)]) == 0 {
			t.Errorf("the path %s as named reaches the root and has no walk", p)
		}
	}
	for key, walks := range id.Walks {
		for _, visits := range walks {
			for _, v := range visits {
				if !w.seen[v.At] {
					t.Errorf("%s: the lookup of %s was not announced before it was made", key, v.At)
				}
				if v != Look(v.At) {
					t.Errorf("%s: the visit %+v is not what is there: %+v", key, v, Look(v.At))
				}
			}
		}
	}
}
