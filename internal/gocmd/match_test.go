package gocmd

import (
	"bytes"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestPackageFlagsAgreeWithGo: for seeded go build command lines over a
// module of Go packages (package patterns, and -gcflags values with and
// without a pattern, on the line and in GOFLAGS), PackageFlags gives each
// package the compiler flags cmd/go gives it, read from the compile
// commands of `go build -n -a`. cmd/go is the reference model: ergo go
// adds its -ergoir to these flags as the last value that matches an Ergo
// package, so a wrong answer drops or adds a user's flag. It runs the go
// command, which compiles nothing under -n; a failing seed prints the
// command line and the replay command.
//
// Mutants: gocmd-gcflags-accumulate (every matching value adds instead of
// the last one winning), gocmd-gcflags-unnamed (a value without a pattern
// applies to no package), gocmd-relative-by-path (a relative pattern
// matches by import path).
func TestPackageFlagsAgreeWithGo(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the go command: the full tier")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go command")
	}
	root := t.TempDir()
	pkgs := []string{".", "a", "a/b", "c"}
	write := func(rel, text string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/m\n\ngo 1.27\n")
	for _, p := range pkgs {
		name := filepath.Base(p)
		if p == "." {
			name = "m"
		}
		write(p+"/x.go", "package "+name+"\n")
	}
	patterns := []string{"", "all", "./a", "./a/...", "./...", "example.com/m/...", "example.com/m/c", "./c", ".", "example.com/m/a"}
	args := []string{"./...", "./a", "./a/...", "example.com/m/c", ".", "./c"}
	flags := []string{"-N", "-l", "-B", "-wb=false"}
	for seed := uint64(1); seed <= 16; seed++ {
		r := rand.New(rand.NewPCG(seed, 7))
		line := []string{"build", "-n", "-a"}
		var gcflags []string
		for range 1 + r.IntN(3) {
			pick := slices.Clone(flags)
			r.Shuffle(len(pick), func(i, j int) { pick[i], pick[j] = pick[j], pick[i] })
			v := strings.Join(pick[:1+r.IntN(len(pick))], " ")
			if pat := patterns[r.IntN(len(patterns))]; pat != "" {
				v = pat + "=" + v
			}
			gcflags = append(gcflags, v)
			if r.IntN(2) == 0 {
				line = append(line, "-gcflags", v)
			} else {
				line = append(line, "-gcflags="+v)
			}
		}
		goflags := ""
		if r.IntN(3) == 0 {
			goflags = "-gcflags=all=-B"
		}
		named := slices.Clone(args)
		r.Shuffle(len(named), func(i, j int) { named[i], named[j] = named[j], named[i] })
		line = append(line, named[:1+r.IntN(2)]...)
		cmd := exec.Command(goBin, line...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GOFLAGS="+goflags, "GOWORK=off", "GOTOOLCHAIN=local")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("seed %d: go %s: %v\n%s", seed, strings.Join(line, " "), err, stderr.String())
		}
		got := compileFlags(stderr.String(), flags)
		parsed, err := Parse(line, goflags)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) == 0 {
			t.Fatalf("seed %d: go %s printed no compile of the module's packages\n%s", seed, strings.Join(line, " "), stderr.String())
		}
		for _, p := range pkgs {
			ip := "example.com/m"
			if p != "." {
				ip += "/" + p
			}
			want, built := got[ip]
			if !built {
				continue
			}
			have, err := parsed.PackageFlags(Package{ImportPath: ip, Dir: filepath.Join(root, filepath.FromSlash(p))}, root)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(have, want) {
				t.Fatalf("seed %d: GOFLAGS=%q go %s\n%s: PackageFlags %q, cmd/go %q\nreplay: go test -count=1 -run '^TestPackageFlagsAgreeWithGo$' ./internal/gocmd", seed, goflags, strings.Join(line, " "), ip, have, want)
			}
		}
	}
}

// compileFlags are, by package, the flags of the set among the arguments
// of each compile command that `go build -n` printed.
func compileFlags(out string, set []string) map[string][]string {
	res := map[string][]string{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || !strings.HasSuffix(f[0], "/compile") {
			continue
		}
		i := slices.Index(f, "-p")
		if i < 0 || i+1 >= len(f) || !strings.HasPrefix(f[i+1], "example.com/m") {
			continue
		}
		flags := []string{}
		for _, a := range f {
			if slices.Contains(set, a) {
				flags = append(flags, a)
			}
		}
		res[f[i+1]] = flags
	}
	return res
}
