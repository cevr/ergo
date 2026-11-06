package build

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// TestCachePublication exercises every publication boundary: concurrent
// identical builders in one process and across processes, a refused input,
// a failed emission, an emission that cannot be staged, a failed Go
// compile, publication interrupted in process and by a killed builder, and
// every kind of missing or corrupt published output. A partial module never
// satisfies a hit, a failure publishes nothing and leaves no staging behind,
// and a valid hit rewrites nothing.
func TestCachePublication(t *testing.T) {
	base := baseInput()

	t.Run("concurrent identical builders", func(t *testing.T) {
		root := cacheRoot(t)
		const builders = 16
		// Every builder misses and emits before any publishes, so all but
		// one lose the rename and must serve the winner's entry.
		var calls, entered atomic.Int32
		start := make(chan struct{})
		emit := emitFor(base, &calls)
		slow := func() ([]File, error) {
			files, err := emit()
			if entered.Add(1) == builders {
				close(start)
			}
			<-start
			return files, err
		}
		dirs := make([]string, builders)
		errs := make([]error, builders)
		var wg sync.WaitGroup
		for i := range builders {
			wg.Add(1)
			go func() {
				defer wg.Done()
				module, err := released(t)(Cache{Root: root}.Module(base, slow))
				dirs[i], errs[i] = module.Dir, err
			}()
		}
		wg.Wait()
		for i := range builders {
			if errs[i] != nil || dirs[i] != dirs[0] {
				t.Fatalf("builder %d: %s %v, want %s", i, dirs[i], errs[i], dirs[0])
			}
		}
		if calls.Load() != builders {
			t.Fatalf("%d emissions, want every builder's %d", calls.Load(), builders)
		}
		mustVerify(t, root, base)
		if got := entries(t, root, "modules"); len(got) != 1 {
			t.Fatalf("modules %v, want one", got)
		}
		if got := entries(t, root, "staging"); len(got) != 0 {
			t.Fatalf("builders left staging %v", got)
		}
	})

	t.Run("concurrent builder processes", func(t *testing.T) {
		root := cacheRoot(t)
		const builders = 6
		outputs := make([][]byte, builders)
		errs := make([]error, builders)
		var wg sync.WaitGroup
		for i := range builders {
			wg.Add(1)
			go func() {
				defer wg.Done()
				outputs[i], errs[i] = helper(root, "publish").Output()
			}()
		}
		wg.Wait()
		want := mustVerify(t, root, base).Dir + "\n"
		for i := range builders {
			if errs[i] != nil || string(outputs[i]) != want {
				t.Fatalf("process %d: %q %v, want %q", i, outputs[i], errs[i], want)
			}
		}
		if got := entries(t, root, "staging"); len(got) != 0 {
			t.Fatalf("processes left staging %v", got)
		}
	})

	t.Run("refused input", func(t *testing.T) {
		root := cacheRoot(t)
		for name, mutate := range map[string]func(*Input){
			"no source":           func(in *Input) { in.Sources = nil },
			"relative source":     func(in *Input) { in.Sources[0].Path = "a.ergo" },
			"source twice":        func(in *Input) { in.Sources[1].Path = in.Sources[0].Path },
			"no compiler":         func(in *Input) { in.Compiler = "" },
			"empty dependency":    func(in *Input) { in.Dependencies[0].Interface = "" },
			"dependency twice":    func(in *Input) { in.Dependencies[1].Name = in.Dependencies[0].Name },
			"unknown mode":        func(in *Input) { in.Mode = "bench" },
			"escaping runtime":    func(in *Input) { in.Runtime[0].Path = "../rt.go" },
			"runtime as manifest": func(in *Input) { in.Runtime[0].Path = ManifestName },
		} {
			in := baseInput()
			mutate(&in)
			var calls atomic.Int32
			if _, err := released(t)(Cache{Root: root}.Module(in, emitFor(in, &calls))); err == nil || calls.Load() != 0 {
				t.Fatalf("%s: err=%v after %d emissions", name, err, calls.Load())
			}
		}
		if got := append(entries(t, root, "modules"), entries(t, root, "staging")...); len(got) != 0 {
			t.Fatalf("refused inputs left %v", got)
		}
	})

	t.Run("failed emission", func(t *testing.T) {
		root := cacheRoot(t)
		failure := errors.New("emission failed")
		if _, err := released(t)(Cache{Root: root}.Module(base, func() ([]File, error) { return nil, failure })); !errors.Is(err, failure) {
			t.Fatalf("err %v", err)
		}
		if got := append(entries(t, root, "modules"), entries(t, root, "staging")...); len(got) != 0 {
			t.Fatalf("a failed emission left %v", got)
		}
		var calls atomic.Int32
		if module, err := released(t)(Cache{Root: root}.Module(base, emitFor(base, &calls))); err != nil || module.Reused || calls.Load() != 1 {
			t.Fatalf("after a failed emission: %+v %v", module, err)
		}
		mustVerify(t, root, base)
	})

	t.Run("emission that cannot be staged", func(t *testing.T) {
		root := cacheRoot(t)
		for name, files := range map[string][]File{
			"escaping path":         {{Path: "../main.go", Data: []byte("x")}},
			"absolute path":         {{Path: "/main.go", Data: []byte("x")}},
			"collides with runtime": {{Path: "rt/rt.go", Data: []byte("x")}},
			"file under a file":     {{Path: "main.go", Data: []byte("x")}, {Path: "main.go/inner.go", Data: []byte("y")}},
			"written twice":         {{Path: "main.go", Data: []byte("x")}, {Path: "main.go", Data: []byte("y")}},
			"names the manifest":    {{Path: ManifestName, Data: []byte("{}")}},
		} {
			if _, err := released(t)(Cache{Root: root}.Module(base, func() ([]File, error) { return files, nil })); err == nil {
				t.Fatalf("%s: published", name)
			}
		}
		if got := append(entries(t, root, "modules"), entries(t, root, "staging")...); len(got) != 0 {
			t.Fatalf("unstageable emissions left %v", got)
		}
	})

	t.Run("failed Go compile", func(t *testing.T) {
		// The emitted module is complete, so it is published and reused;
		// the action fails, writes no executable and is run again on the
		// next build.
		root := cacheRoot(t)
		broken := func() ([]File, error) {
			return []File{{Path: "go.mod", Data: []byte("module ergotest\n\ngo 1.23\n")}, {Path: "main.go", Data: []byte("package main\n\nfunc main() { undefined() }\n")}}, nil
		}
		for pass := range 2 {
			module, err := released(t)(Cache{Root: root}.Module(base, broken))
			if err != nil || module.Reused != (pass == 1) {
				t.Fatalf("pass %d: %+v %v", pass, module, err)
			}
			output, err := link(t, hostToolchain(), module)
			if err == nil || !strings.Contains(err.Error(), "undefined") {
				t.Fatalf("pass %d: compile gave %v", pass, err)
			}
			if _, err := os.Stat(output); err == nil {
				t.Fatalf("pass %d: a failed compile wrote %s", pass, output)
			}
		}
	})

	t.Run("publication interrupted in process", func(t *testing.T) {
		root := cacheRoot(t)
		interrupted := errors.New("interrupted")
		testHookStaged = func(staging string) error {
			if _, err := os.Stat(filepath.Join(staging, ManifestName)); err != nil {
				t.Errorf("staging is incomplete at the hook: %v", err)
			}
			return interrupted
		}
		_, err := released(t)(Cache{Root: root}.Module(base, emitFor(base, nil)))
		testHookStaged = nil
		if !errors.Is(err, interrupted) {
			t.Fatalf("err %v", err)
		}
		if got := append(entries(t, root, "modules"), entries(t, root, "staging")...); len(got) != 0 {
			t.Fatalf("an interrupted publication left %v", got)
		}
		var calls atomic.Int32
		if module, err := released(t)(Cache{Root: root}.Module(base, emitFor(base, &calls))); err != nil || module.Reused || calls.Load() != 1 {
			t.Fatalf("after interruption: %+v %v", module, err)
		}
		mustVerify(t, root, base)
	})

	t.Run("builder killed before publication", func(t *testing.T) {
		root := cacheRoot(t)
		out, err := helper(root, "crash").CombinedOutput()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 3 {
			t.Fatalf("crash helper: %v %s", err, out)
		}
		if got := entries(t, root, "modules"); len(got) != 0 {
			t.Fatalf("a killed builder published %v", got)
		}
		left := entries(t, root, "staging")
		if len(left) != 1 {
			t.Fatalf("a killed builder left staging %v, want its one directory", left)
		}
		var calls atomic.Int32
		module, err := released(t)(Cache{Root: root}.Module(base, emitFor(base, &calls)))
		if err != nil || module.Reused || calls.Load() != 1 {
			t.Fatalf("after a killed builder: %+v %v, %d emissions", module, err, calls.Load())
		}
		mustVerify(t, root, base)
		if got := entries(t, root, "staging"); len(got) != 1 || got[0] != left[0] {
			t.Fatalf("staging %v, want only the killed builder's %v", got, left)
		}
	})

	t.Run("valid hit rewrites nothing", func(t *testing.T) {
		root := cacheRoot(t)
		published, err := released(t)(Cache{Root: root}.Module(base, emitFor(base, nil)))
		if err != nil {
			t.Fatal(err)
		}
		before := snapshot(t, published.Dir)
		for range 3 {
			var calls atomic.Int32
			module, err := released(t)(Cache{Root: root}.Module(base, emitFor(base, &calls)))
			if err != nil || !module.Reused || calls.Load() != 0 || module.Dir != published.Dir {
				t.Fatalf("hit: %+v %v", module, err)
			}
		}
		if !unchanged(before, snapshot(t, published.Dir)) {
			t.Fatal("a hit rewrote the module")
		}
	})

	t.Run("recovery keeps a valid replacement", func(t *testing.T) {
		// A builder saw a corrupt entry, and another replaced it with a
		// valid one before the first moved it aside: the valid module stays.
		root := cacheRoot(t)
		published, err := released(t)(Cache{Root: root}.Module(base, emitFor(base, nil)))
		if err != nil {
			t.Fatal(err)
		}
		summary, err := base.Summarize()
		if err != nil {
			t.Fatal(err)
		}
		if err := (Cache{Root: root}).discard(published.Dir, published.Key, summary); err != nil {
			t.Fatal(err)
		}
		mustVerify(t, root, base)
		if got := entries(t, root, "staging"); len(got) != 0 {
			t.Fatalf("staging %v", got)
		}
	})

	t.Run("missing or corrupt outputs", func(t *testing.T) {
		manifestOf := func(dir string) string { return filepath.Join(dir, ManifestName) }
		rewriteManifest := func(t *testing.T, dir string, edit func(string) string) {
			data, err := os.ReadFile(manifestOf(dir))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(manifestOf(dir), []byte(edit(string(data))), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		for name, corrupt := range map[string]func(t *testing.T, dir string){
			"output removed":     func(t *testing.T, dir string) { mustRemove(t, filepath.Join(dir, "main.go")) },
			"runtime removed":    func(t *testing.T, dir string) { mustRemove(t, filepath.Join(dir, "rt", "rt.go")) },
			"output truncated":   func(t *testing.T, dir string) { mustWrite(t, filepath.Join(dir, "main.go"), "package main\n") },
			"output altered":     func(t *testing.T, dir string) { appendTo(t, filepath.Join(dir, "main.go"), "// edited\n") },
			"extra file":         func(t *testing.T, dir string) { mustWrite(t, filepath.Join(dir, "extra.go"), "package main\n") },
			"manifest removed":   func(t *testing.T, dir string) { mustRemove(t, manifestOf(dir)) },
			"manifest truncated": func(t *testing.T, dir string) { mustWrite(t, manifestOf(dir), "{\"schema\":") },
			"manifest incomplete": func(t *testing.T, dir string) {
				rewriteManifest(t, dir, func(s string) string { return strings.Replace(s, "\"complete\": true", "\"complete\": false", 1) })
			},
			"manifest of another key": func(t *testing.T, dir string) {
				rewriteManifest(t, dir, func(s string) string { return strings.Replace(s, "\"key\": \"", "\"key\": \"0", 1) })
			},
			"manifest of another input": func(t *testing.T, dir string) {
				rewriteManifest(t, dir, func(s string) string { return strings.Replace(s, "compiler-1", "compiler-9", 1) })
			},
			"output replaced by a link": func(t *testing.T, dir string) {
				target := filepath.Join(t.TempDir(), "main.go")
				data, err := os.ReadFile(filepath.Join(dir, "main.go"))
				if err != nil {
					t.Fatal(err)
				}
				mustWrite(t, target, string(data))
				mustRemove(t, filepath.Join(dir, "main.go"))
				if err := os.Symlink(target, filepath.Join(dir, "main.go")); err != nil {
					t.Fatal(err)
				}
			},
			"entry replaced by a file": func(t *testing.T, dir string) {
				if err := os.RemoveAll(dir); err != nil {
					t.Fatal(err)
				}
				mustWrite(t, dir, "not a module")
			},
			"partial entry from a non-atomic writer": func(t *testing.T, dir string) {
				if err := os.RemoveAll(dir); err != nil {
					t.Fatal(err)
				}
				mustWrite(t, filepath.Join(dir, "go.mod"), "module ergotest\n")
			},
		} {
			t.Run(name, func(t *testing.T) {
				root := cacheRoot(t)
				published, err := released(t)(Cache{Root: root}.Module(base, emitFor(base, nil)))
				if err != nil {
					t.Fatal(err)
				}
				corrupt(t, published.Dir)
				var calls atomic.Int32
				module, err := released(t)(Cache{Root: root}.Module(base, emitFor(base, &calls)))
				if err != nil || module.Reused || calls.Load() != 1 || module.Dir != published.Dir {
					t.Fatalf("a corrupt entry was served: %+v %v, %d emissions", module, err, calls.Load())
				}
				mustVerify(t, root, base)
				if got := entries(t, root, "staging"); len(got) != 0 {
					t.Fatalf("recovery left staging %v", got)
				}
				var after atomic.Int32
				if again, err := released(t)(Cache{Root: root}.Module(base, emitFor(base, &after))); err != nil || !again.Reused || after.Load() != 0 {
					t.Fatalf("the replaced entry is not reused: %+v %v", again, err)
				}
			})
		}
	})
}

// helper is the test binary started as a publication helper.
func helper(root, mode string) *exec.Cmd {
	child := exec.Command(os.Args[0], "-test.run=^$")
	child.Env = append(os.Environ(), helperVariable+"="+mode, rootVariable+"="+root)
	return child
}

func mustWrite(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustRemove(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

func appendTo(t *testing.T, path, data string) {
	t.Helper()
	old, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, path, string(bytes.Clone(old))+data)
}
