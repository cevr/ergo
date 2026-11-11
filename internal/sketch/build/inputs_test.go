package build

import (
	"bytes"
	"debug/elf"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

// inputMutation changes one field of the base input. covers names the
// schema fields it changes as Type.Field. A canonical mutation (same) keeps
// the meaning, such as an order the key sorts, and must keep the key and
// reuse the module.
type inputMutation struct {
	name        string
	covers      []string
	apply       func(*Input)
	same        bool
	runtimeName string
}

var inputMutations = []inputMutation{
	{name: "source bytes", covers: []string{"Source.Text"}, apply: func(in *Input) { in.Sources[0].Text = []byte("func main() { }\n") }},
	{name: "source renamed with the same bytes", covers: []string{"Source.Path"}, apply: func(in *Input) { in.Sources[1].Path = "/src/app/c.ergo" }},
	{name: "same bytes under another directory", covers: []string{"Source.Path"}, apply: func(in *Input) { in.Sources[0].Path = "/src/other/a.ergo" }},
	{name: "source added", covers: []string{"Input.Sources"}, apply: func(in *Input) {
		in.Sources = append(in.Sources, Source{Path: "/src/app/d.ergo", Text: []byte("func more() {}\n")})
	}},
	{name: "source removed", covers: []string{"Input.Sources"}, apply: func(in *Input) { in.Sources = in.Sources[:1] }},
	{name: "sources reordered", same: true, apply: func(in *Input) { slices.Reverse(in.Sources) }},
	{name: "dependency interface (public change)", covers: []string{"Dependency.Interface"}, apply: func(in *Input) { in.Dependencies[1].Interface = "iface-timeout-2" }},
	{name: "dependency implementation (private change)", covers: []string{"Dependency.Implementation"}, apply: func(in *Input) { in.Dependencies[1].Implementation = "impl-timeout-2" }},
	{name: "dependency renamed", covers: []string{"Dependency.Name"}, apply: func(in *Input) { in.Dependencies[1].Name = "task" }},
	{name: "dependency added", covers: []string{"Input.Dependencies"}, apply: func(in *Input) {
		in.Dependencies = append(in.Dependencies, Dependency{Name: "queue", Interface: "iface-queue-1", Implementation: "impl-queue-1"})
	}},
	{name: "dependency removed", covers: []string{"Input.Dependencies"}, apply: func(in *Input) { in.Dependencies = in.Dependencies[:1] }},
	{name: "dependencies reordered", same: true, apply: func(in *Input) { slices.Reverse(in.Dependencies) }},
	{name: "compiler identity", covers: []string{"Input.Compiler"}, apply: func(in *Input) { in.Compiler = "sha256:compiler-2" }},
	{name: "emitter revision", covers: []string{"Input.Emitter"}, apply: func(in *Input) { in.Emitter = "test-emit/2" }},
	{name: "runtime edit", covers: []string{"File.Data"}, runtimeName: "r2", apply: func(in *Input) {
		in.Runtime[0].Data = []byte("package rt\n\nconst Name = \"r2\"\n")
	}},
	{name: "runtime file moved", covers: []string{"File.Path"}, apply: func(in *Input) { in.Runtime[0].Path = "rt/name.go" }},
	{name: "runtime file added", covers: []string{"Input.Runtime"}, apply: func(in *Input) {
		in.Runtime = append(in.Runtime, File{Path: "rt/more.go", Data: []byte("package rt\n\nconst More = 1\n")})
	}},
	{name: "runtime file removed", covers: []string{"Input.Runtime"}, apply: func(in *Input) { in.Runtime = in.Runtime[:1] }},
	{name: "runtime files reordered", same: true, apply: func(in *Input) { slices.Reverse(in.Runtime) }},
	{name: "language revision", covers: []string{"Input.Language"}, apply: func(in *Input) { in.Language = "ergo-v2-test" }},
	{name: "test mode", covers: []string{"Input.Mode"}, apply: func(in *Input) { in.Mode = ModeTest }},
	{name: "emission flag", covers: []string{"Input.Flags"}, apply: func(in *Input) { in.Flags = []string{"-race"} }},
}

// TestCacheInputs mutates every field of the emission input schema and
// checks the cache's actual verdict and artifact, not a hash helper: each
// mutation misses, emits once and publishes a separate module whose
// executable prints the mutated input, while the base module stays
// published and is reused without a rewrite; each canonical mutation hits.
// Then every field of the Go build action is mutated: the emitted module is
// reused and the action, and its executable when behavior changes, follow
// the mutation. A schema field without a mutation fails the test.
func TestCacheInputs(t *testing.T) {
	covered := map[string]bool{}
	t.Run("emission", func(t *testing.T) {
		root := t.TempDir()
		base := baseInput()
		var baseCalls atomic.Int32
		published, err := Cache{Root: root}.Module(base, emitFor(base, &baseCalls))
		if err != nil || published.Reused || baseCalls.Load() != 1 {
			t.Fatalf("cold base: %+v %v, %d emissions", published, err, baseCalls.Load())
		}
		baseBinary, err := link(t, hostToolchain(), published)
		if err != nil {
			t.Fatal(err)
		}
		baseOutput := runProgram(t, baseBinary)
		if want := expectedOutput(t, base, "r1", "unset"); baseOutput != want {
			t.Fatalf("base prints %q, want %q", baseOutput, want)
		}
		before := snapshot(t, published.Dir)
		for _, m := range inputMutations {
			t.Run(m.name, func(t *testing.T) {
				for _, c := range m.covers {
					covered[c] = true
				}
				mutated := baseInput()
				m.apply(&mutated)
				var calls atomic.Int32
				module, err := Cache{Root: root}.Module(mutated, emitFor(mutated, &calls))
				if err != nil {
					t.Fatal(err)
				}
				if m.same {
					if !module.Reused || calls.Load() != 0 || module.Dir != published.Dir {
						t.Fatalf("canonical mutation missed: reused=%v emissions=%d dir=%s", module.Reused, calls.Load(), module.Dir)
					}
					return
				}
				if module.Reused || calls.Load() != 1 || module.Dir == published.Dir || module.Key == published.Key {
					t.Fatalf("mutation hit: reused=%v emissions=%d dir=%s", module.Reused, calls.Load(), module.Dir)
				}
				mustVerify(t, root, mutated)
				binary, err := link(t, hostToolchain(), module)
				if err != nil {
					t.Fatal(err)
				}
				name := m.runtimeName
				if name == "" {
					name = "r1"
				}
				got := runProgram(t, binary)
				if want := expectedOutput(t, mutated, name, "unset"); got != want || got == baseOutput {
					t.Fatalf("mutated executable prints %q, want %q (base %q)", got, want, baseOutput)
				}
				again, err := Cache{Root: root}.Module(base, emitFor(base, &baseCalls))
				if err != nil || !again.Reused || again.Dir != published.Dir || baseCalls.Load() != 1 || !unchanged(before, snapshot(t, published.Dir)) {
					t.Fatalf("base after mutation: reused=%v emissions=%d err=%v", again.Reused, baseCalls.Load(), err)
				}
			})
		}
	})
	t.Run("action", func(t *testing.T) {
		root := t.TempDir()
		base := baseInput()
		var calls atomic.Int32
		module, err := Cache{Root: root}.Module(base, emitFor(base, &calls))
		if err != nil {
			t.Fatal(err)
		}
		baseCommand := hostToolchain().Command(module, "/out/program")
		if baseCommand.Dir != module.Dir || !slices.Contains(baseCommand.Env, "GOWORK=off") ||
			!slices.Equal(baseCommand.Args[len(baseCommand.Args)-3:], []string{"-o", "/out/program", "."}) {
			t.Fatalf("base action %v in %s", baseCommand.Args, baseCommand.Dir)
		}
		baseBinary, err := link(t, hostToolchain(), module)
		if err != nil {
			t.Fatal(err)
		}
		baseBytes, err := os.ReadFile(baseBinary)
		if err != nil {
			t.Fatal(err)
		}
		// reuse links tc against the base input's module, which must be
		// served from the cache without emitting, and returns the binary.
		reuse := func(t *testing.T, tc Toolchain) []byte {
			t.Helper()
			again, err := Cache{Root: root}.Module(base, emitFor(base, &calls))
			if err != nil || !again.Reused || calls.Load() != 1 {
				t.Fatalf("an action change re-emitted: reused=%v emissions=%d err=%v", again.Reused, calls.Load(), err)
			}
			command := tc.Command(again, "/out/program")
			if slices.Equal(command.Env, baseCommand.Env) && command.Path == baseCommand.Path {
				t.Fatal("the action is unchanged")
			}
			binary, err := link(t, tc, again)
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(binary)
			if err != nil {
				t.Fatal(err)
			}
			return data
		}
		t.Run("GOFLAGS", func(t *testing.T) {
			covered["Toolchain.GOFLAGS"] = true
			tc := hostToolchain()
			tc.GOFLAGS = "-ldflags=-X=main.flag=set"
			data := reuse(t, tc)
			binary := filepath.Join(t.TempDir(), "flagged")
			if err := os.WriteFile(binary, data, 0o755); err != nil {
				t.Fatal(err)
			}
			if got, want := runProgram(t, binary), expectedOutput(t, base, "r1", "set"); got != want {
				t.Fatalf("flagged executable prints %q, want %q", got, want)
			}
		})
		t.Run("GOARCH", func(t *testing.T) {
			covered["Toolchain.GOARCH"] = true
			tc := hostToolchain()
			tc.GOOS, tc.GOARCH = "linux", "arm64"
			if runtime.GOARCH == "arm64" {
				tc.GOARCH = "amd64"
			}
			data := reuse(t, tc)
			file, err := elf.NewFile(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]elf.Machine{"arm64": elf.EM_AARCH64, "amd64": elf.EM_X86_64}[tc.GOARCH]
			if file.Machine != want || bytes.Equal(data, baseBytes) {
				t.Fatalf("GOARCH=%s linked machine %v", tc.GOARCH, file.Machine)
			}
		})
		t.Run("GOOS", func(t *testing.T) {
			covered["Toolchain.GOOS"] = true
			tc := hostToolchain()
			tc.GOOS = "windows"
			if runtime.GOOS == "windows" {
				tc.GOOS = "linux"
			}
			data := reuse(t, tc)
			isPE := bytes.HasPrefix(data, []byte("MZ"))
			if bytes.Equal(data, baseBytes) || isPE != (tc.GOOS == "windows") {
				t.Fatalf("GOOS=%s linked the base executable format", tc.GOOS)
			}
		})
		t.Run("Go", func(t *testing.T) {
			covered["Toolchain.Go"] = true
			if runtime.GOOS == "windows" {
				t.Skip("the toolchain wrapper is a shell script")
			}
			realGo, err := exec.LookPath("go")
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			marker := filepath.Join(dir, "invoked")
			wrapper := filepath.Join(dir, "go-wrapper")
			script := "#!/bin/sh\nprintf invoked >> '" + marker + "'\nexec '" + realGo + "' \"$@\"\n"
			if strings.Contains(dir+realGo, "'") {
				t.Skip("a path holds a quote")
			}
			if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			tc := hostToolchain()
			tc.Go = wrapper
			data := reuse(t, tc)
			if got, err := os.ReadFile(marker); err != nil || string(got) != "invoked" {
				t.Fatalf("the action did not run the named toolchain: %q %v", got, err)
			}
			binary := filepath.Join(t.TempDir(), "wrapped")
			if err := os.WriteFile(binary, data, 0o755); err != nil {
				t.Fatal(err)
			}
			if got, want := runProgram(t, binary), expectedOutput(t, base, "r1", "unset"); got != want {
				t.Fatalf("wrapped toolchain's executable prints %q, want %q", got, want)
			}
		})
		t.Run("Tags", func(t *testing.T) {
			// `ergo test --explore` (runtime.md, Explored schedules): the
			// tag is the action's alone. One emitted module, never emitted
			// again, links its live runtime without the tag and its
			// explored runtime with it.
			covered["Toolchain.Tags"] = true
			in := baseInput()
			in.Runtime = []File{
				{Path: "rt/live.go", Data: []byte("//go:build !ergoexplore\n\npackage rt\n\nconst Name = \"live\"\n")},
				{Path: "rt/explore.go", Data: []byte("//go:build ergoexplore\n\npackage rt\n\nconst Name = \"explored\"\n")},
			}
			var emitted atomic.Int32
			module, err := Cache{Root: root}.Module(in, emitFor(in, &emitted))
			if err != nil {
				t.Fatal(err)
			}
			tc := hostToolchain()
			tc.Tags = []string{"ergoexplore"}
			for _, c := range []struct {
				tc   Toolchain
				name string
			}{{hostToolchain(), "live"}, {tc, "explored"}} {
				again, err := Cache{Root: root}.Module(in, emitFor(in, &emitted))
				if err != nil || again.Dir != module.Dir || emitted.Load() != 1 {
					t.Fatalf("the tag changed the emission: dir %s (want %s), %d emissions, %v", again.Dir, module.Dir, emitted.Load(), err)
				}
				binary, err := link(t, c.tc, again)
				if err != nil {
					t.Fatal(err)
				}
				if got, want := runProgram(t, binary), expectedOutput(t, in, c.name, "unset"); got != want {
					t.Fatalf("tags %v: the executable prints %q, want %q", c.tc.Tags, got, want)
				}
			}
		})
		t.Run("module", func(t *testing.T) {
			// The action links whatever module it is given: another input's
			// module is another action.
			test := baseInput()
			test.Mode = ModeTest
			other, err := Cache{Root: root}.Module(test, emitFor(test, nil))
			if err != nil {
				t.Fatal(err)
			}
			if command := hostToolchain().Command(other, "/out/program"); command.Dir == baseCommand.Dir {
				t.Fatal("two modules share an action directory")
			}
		})
	})
	for _, v := range []any{Input{}, Source{}, Dependency{}, File{}, Toolchain{}} {
		typ := reflect.TypeOf(v)
		for i := 0; i < typ.NumField(); i++ {
			if name := typ.Name() + "." + typ.Field(i).Name; !covered[name] {
				t.Errorf("schema field %s has no mutation", name)
			}
		}
	}
}
