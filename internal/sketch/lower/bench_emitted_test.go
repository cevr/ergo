package lower

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// emittedBench measures emitted Go in process, as docs/receipts/lower-v1.md
// does: it lowers src (a program whose main uses Console, so the emitted
// package declares Env), writes it as package p/NAME of a fresh module
// with the Go test file that benchGo returns for the emitted main.go, and
// compiles that test binary once. Each name in benches is then a
// sub-benchmark of b that runs the binary's Benchmark<name> for b.N
// iterations and reports its ns/op, B/op and allocs/op. The Go control
// is a Benchmark in the same file, so both sides run in one kind of
// process. benchEnv, an *Env over a root task, is declared for the calls
// that take one (emittedCall).
//
// It builds Go because a benchmark's question is the speed of the built
// program against its Go control.
func emittedBench(b *testing.B, name, src string, benchGo func(mainGo string) string, benches ...string) {
	b.Helper()
	o := lowerSource(name, src, true)
	if o.err != nil || len(o.diags) > 0 || o.panicked != nil || len(o.refused) > 0 {
		b.Fatalf("%s does not lower: %v %v %v %v", name, o.err, o.diags, o.panicked, o.refused)
	}
	root := b.TempDir()
	write := func(rel string, data []byte) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			b.Fatal(err)
		}
	}
	write("go.mod", []byte(goMod))
	for _, f := range Runtime() {
		write(f.Path, f.Data)
	}
	write("p/"+name+"/main.go", []byte(o.mainGo()))
	write("p/"+name+"/bench_env_test.go", []byte("package main\n\nimport rt \"ergo.local/prototype/runtime/rt\"\n\nvar benchEnv = &Env{task: rt.Root(\"bench\")}\n"))
	write("p/"+name+"/bench_test.go", []byte(benchGo(o.mainGo())))
	exe := filepath.Join(root, name+".test")
	cmd := exec.Command("go", "test", "-c", "-trimpath", "-o", exe, "./p/"+name)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		b.Fatalf("build the benchmark of %s: %v\n%s\nGo:\n%s", name, err, out, o.mainGo())
	}
	line := regexp.MustCompile(`([0-9.]+) ns/op\s+([0-9.]+) B/op\s+([0-9.]+) allocs/op`)
	for _, bench := range benches {
		b.Run(bench, func(b *testing.B) {
			// ReportAllocs makes the framework print B/op and allocs/op;
			// ReportMetric below sets them to the binary's values.
			b.ReportAllocs()
			run := exec.Command(exe, "-test.run", "^$", "-test.bench", "^Benchmark"+bench+"$", "-test.benchmem", "-test.benchtime", strconv.Itoa(b.N)+"x", "-test.count", "1")
			var out bytes.Buffer
			run.Stdout, run.Stderr = &out, &out
			if err := run.Run(); err != nil {
				b.Fatalf("%s: %v\n%s", bench, err, out.String())
			}
			m := line.FindStringSubmatch(out.String())
			if m == nil {
				b.Fatalf("%s: no result line in\n%s", bench, out.String())
			}
			for i, unit := range []string{"ns/op", "B/op", "allocs/op"} {
				v, err := strconv.ParseFloat(m[i+1], 64)
				if err != nil {
					b.Fatal(err)
				}
				b.ReportMetric(v, unit)
			}
		})
	}
}

// emittedCall is a call of the emitted function fn with args: with
// benchEnv first when the lowering gave fn a hidden environment.
func emittedCall(mainGo, fn, args string) string {
	if strings.Contains(mainGo, "func "+fn+"(env *Env") {
		if args == "" {
			return fn + "(benchEnv)"
		}
		return fn + "(benchEnv, " + args + ")"
	}
	return fn + "(" + args + ")"
}
