package sketchfront

import (
	"bytes"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/build/exetest"
)

// TestDirectMatchesGoBuild is the identity property of the direct build
// path (internal/sketch/build, direct.go): for every example program and
// seeded generated programs, the direct path's executable is go build's
// but for the build ID. It lowers Ergo programs, so it lives here and not
// in internal/sketch/build, whose tests then stay off the checker (a
// checker edit reran them in the fast tier: batch-18 verify B1). It builds
// Go because the property is about the executables the two paths write. A
// failing case names the program (a generated one prints its source and
// its seed; replay with ERGO_DIRECT_SEED=N).
func TestDirectMatchesGoBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
	c := build.Cache{Root: t.TempDir()}
	tc := build.Toolchain{Go: "go"}
	var programs []string
	root, err := filepath.Abs(filepath.Join("..", "..", "examples"))
	if err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !e.IsDir() && e.Name() == "main.ergo" {
			programs = append(programs, filepath.Dir(path))
		}
		return nil
	})
	if err != nil || len(programs) == 0 {
		t.Fatalf("no example programs: %v", err)
	}
	seeds := []uint64{1, 2, 3, 4, 5, 6, 7, 8}
	if s := os.Getenv("ERGO_DIRECT_SEED"); s != "" {
		var seed uint64
		fmt.Sscan(s, &seed)
		seeds, programs = []uint64{seed}, nil
	}
	for _, seed := range seeds {
		dir := filepath.Join(t.TempDir(), fmt.Sprintf("generated%d", seed))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "main.ergo"), []byte(directProgram(seed)), 0o644); err != nil {
			t.Fatal(err)
		}
		programs = append(programs, dir)
	}
	for _, dir := range programs {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			if strings.HasPrefix(filepath.Base(dir), "generated") {
				source, _ := os.ReadFile(filepath.Join(dir, "main.ergo"))
				defer func() {
					if t.Failed() {
						t.Logf("generated program (replay with ERGO_DIRECT_SEED):\n%s", source)
					}
				}()
			}
			a, err := CheckDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			absolute, err := filepath.Abs(dir)
			if err != nil {
				t.Fatal(err)
			}
			// Both link modes: unstripped (ergo run, --debug) and
			// stripped (ergo build's default).
			for _, strip := range []bool{false, true} {
				out := t.TempDir()
				var report build.Report
				var stderr bytes.Buffer
				for _, path := range []struct {
					out     string
					goBuild bool
				}{{"direct", false}, {"go", true}} {
					req := BuildRequest{Mode: build.ModeBuild, Source: absolute, Output: filepath.Join(out, path.out), Cache: c, Toolchain: tc, Stderr: &stderr}
					req.Toolchain.GoBuild = path.goBuild
					req.Toolchain.Strip = strip
					if !path.goBuild {
						req.Report = &report
					}
					if _, err := Build(a, req); err != nil {
						t.Fatalf("%s, strip %t: %v\n%s", path.out, strip, err, stderr.String())
					}
				}
				if !report.Direct {
					if !strings.HasPrefix(report.Refused, "direct build refused:") {
						t.Fatalf("strip %t: the direct path failed: %s", strip, report.Refused)
					}
					t.Logf("strip %t: go build: %s", strip, report.Refused)
				}
				exetest.Same(t, fmt.Sprintf("%s (strip %t)", dir, strip), filepath.Join(out, "direct"), filepath.Join(out, "go"))
			}
		})
	}
}

// directProgram is a seeded program: a chain of string functions, some of
// them calling the standard strings and strconv packages, so seeds vary
// the standard imports, the main package's size and its text.
func directProgram(seed uint64) string {
	r := rand.New(rand.NewPCG(seed, 0x6469726563740a))
	useStrings, useStrconv := r.IntN(2) == 0, r.IntN(2) == 0
	var b strings.Builder
	if useStrings {
		b.WriteString("import strings\n")
	}
	if useStrconv {
		b.WriteString("import strconv\n")
	}
	b.WriteString("\n")
	n := 1 + r.IntN(12)
	for i := range n {
		body := fmt.Sprintf("s + %q", fmt.Sprintf("-%d.%d", seed, r.IntN(1000)))
		switch k := r.IntN(3); {
		case k == 1 && useStrings:
			body = "strings.TrimSpace(" + body + ")"
		case k == 2 && useStrconv:
			body = fmt.Sprintf("s + strconv.Itoa(%d)", r.IntN(1<<20))
		}
		fmt.Fprintf(&b, "func f%d(s string) string { %s }\n\n", i, body)
	}
	call := fmt.Sprintf("%q", fmt.Sprintf("seed %d", seed))
	for i := range n {
		call = fmt.Sprintf("f%d(%s)", i, call)
	}
	fmt.Fprintf(&b, "func main() needs Console {\n\tConsole.Println(%s)\n}\n", call)
	return b.String()
}
