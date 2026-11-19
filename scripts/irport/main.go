// Command irport ports the IR build path's compiler patch to a Go release
// (plan-tier §1.5, PORT-<ver>) and prints its row for toolchain/ir/ports.md.
//
//	go run ./scripts/irport -goroot DIR -work DIR [-patch FILE] [-examples a,b] [-noder]
//
// -goroot is a pristine Go installation with its source (a golang.org/toolchain
// module copy is one); it is read only. irport copies it into -work (which
// must not exist), checks the patch against toolchain/ir/lock, applies it
// (counting rejected hunks), builds the patched cmd/compile, runs the noder
// tests with -noder, and checks stock equivalence: without -ergoir the
// patched compiler builds every example (or the -examples named) byte for
// byte as the pristine one does. It exits 1 when any step fails.
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"ergo.local/prototype/internal/experimental/irspike"
	"ergo.local/prototype/internal/toolchain"
)

func main() {
	goroot := flag.String("goroot", "", "a pristine Go installation with its source (read only)")
	work := flag.String("work", "", "a new directory for the patched copy and its caches")
	patch := flag.String("patch", "toolchain/ir/go1.27.0.patch", "the compiler patch")
	lock := flag.String("lock", "toolchain/ir/lock", "the lock the patch must match; empty to skip (a planted patch)")
	examples := flag.String("examples", "", "comma-separated example binary folders (default: every example binary)")
	noder := flag.Bool("noder", false, "run go test -short cmd/compile/internal/noder in the patched copy")
	flag.Parse()
	if *goroot == "" || *work == "" {
		fmt.Fprintln(os.Stderr, "usage: irport -goroot DIR -work DIR [-patch FILE] [-lock FILE] [-examples a,b] [-noder]")
		os.Exit(2)
	}
	r, err := port(*goroot, *work, *patch, *lock, *examples, *noder)
	fmt.Println(r.row())
	if err != nil {
		fmt.Fprintln(os.Stderr, "irport:", err)
		os.Exit(1)
	}
}

// result is one port, a row of ports.md.
type result struct {
	goVersion, pkgbits     string
	hunks, rejected, lines int
	files                  []string
	noder                  string
	equal, built           int
	differ                 []string
}

func (r result) row() string {
	return fmt.Sprintf("| %s | %d of %d hunks rejected | %d | %d: %s | %s | %s | %d of %d examples%s |",
		r.goVersion, r.rejected, r.hunks, r.lines, len(r.files), strings.Join(r.files, ", "), r.pkgbits, r.noder, r.equal, r.built, differNote(r.differ))
}

func differNote(d []string) string {
	if len(d) == 0 {
		return ""
	}
	return " (differ: " + strings.Join(d, ", ") + ")"
}

func port(goroot, work, patchFile, lockFile, examples string, noder bool) (result, error) {
	r := result{noder: "not run"}
	data, err := os.ReadFile(patchFile)
	if err != nil {
		return r, err
	}
	if lockFile != "" {
		if err := checkLock(lockFile, filepath.Base(patchFile), data); err != nil {
			return r, err
		}
	}
	r.hunks, r.lines, r.files = patchShape(data)
	ver, err := os.ReadFile(filepath.Join(goroot, "VERSION"))
	if err != nil {
		return r, err
	}
	r.goVersion, _, _ = strings.Cut(string(ver), "\n")
	if _, err := os.Stat(work); err == nil {
		return r, fmt.Errorf("%s exists; name a new directory", work)
	}
	patched := filepath.Join(work, "goroot")
	if err := os.CopyFS(patched, os.DirFS(goroot)); err != nil {
		return r, fmt.Errorf("copy %s: %w", goroot, err)
	}
	// ergo's own applier (internal/toolchain): the row says what ergo
	// does when it builds the patched compile from this release.
	applied, err := toolchain.ApplyPatch(goroot, data)
	if err != nil {
		return r, err
	}
	r.rejected = len(applied.Rejected)
	if r.rejected > 0 {
		return r, fmt.Errorf("rejected hunks: %s", strings.Join(applied.Rejected, "; "))
	}
	for name, content := range applied.Files {
		file := filepath.Join(patched, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			return r, err
		}
		os.Chmod(file, 0o644) // a module copy's files are read only
		if err := os.WriteFile(file, content, 0o644); err != nil {
			return r, err
		}
	}
	r.pkgbits = pkgbitsVersion(patched)
	env := goEnv(patched, filepath.Join(work, "gocache-build"))
	if err := run(env, filepath.Join(patched, "src"), filepath.Join(patched, "bin", "go"), "install", "cmd/compile"); err != nil {
		return r, fmt.Errorf("build cmd/compile: %w", err)
	}
	if noder {
		r.noder = "ok"
		if err := run(env, filepath.Join(patched, "src"), filepath.Join(patched, "bin", "go"), "test", "-short", "-count=1", "cmd/compile/internal/noder"); err != nil {
			r.noder = "FAIL"
			return r, fmt.Errorf("noder tests: %w", err)
		}
	}
	return r, stockEquivalence(&r, goroot, patched, work, examples)
}

// checkLock requires the patch's sha256 to be the lock's.
func checkLock(lockFile, name string, data []byte) error {
	f, err := os.ReadFile(lockFile)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	want := "patch " + name + " sha256:" + hex.EncodeToString(sum[:])
	for _, line := range strings.Split(string(f), "\n") {
		if strings.TrimSpace(line) == want {
			return nil
		}
	}
	return fmt.Errorf("%s does not lock %s at its content (%s)", lockFile, name, want)
}

// patchShape counts a unified diff's hunks, its changed lines and the
// files it touches.
func patchShape(data []byte) (hunks, lines int, files []string) {
	s := bufio.NewScanner(bytes.NewReader(data))
	s.Buffer(make([]byte, 1<<20), 1<<20)
	for s.Scan() {
		l := s.Text()
		switch {
		case strings.HasPrefix(l, "+++ b/"):
			files = append(files, strings.TrimPrefix(l, "+++ b/src/cmd/compile/internal/"))
		case strings.HasPrefix(l, "@@ "):
			hunks++
		case strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "-") && !strings.HasPrefix(l, "---"):
			lines++
		}
	}
	return hunks, lines, files
}

// pkgbitsVersion is the newest unified export format version the release
// declares (internal/pkgbits/version.go).
func pkgbitsVersion(goroot string) string {
	data, err := os.ReadFile(filepath.Join(goroot, "src", "internal", "pkgbits", "version.go"))
	if err != nil {
		return "pkgbits ?"
	}
	m := regexp.MustCompile(`(?m)^\t(V\d+)\b`).FindAllSubmatch(data, -1)
	if len(m) == 0 {
		return "pkgbits ?"
	}
	return "pkgbits " + string(m[len(m)-1][1])
}

func goEnv(goroot, cache string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "GOROOT", "GOCACHE", "GOFLAGS", "GOTOOLCHAIN", "GOWORK", "PATH":
			continue
		}
		env = append(env, kv)
	}
	return append(env, "GOROOT="+goroot, "GOCACHE="+cache, "GOFLAGS=", "GOTOOLCHAIN=local", "GOWORK=off",
		"PATH="+filepath.Join(goroot, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func run(env []string, dir, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir, cmd.Env = dir, env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v\n%s", filepath.Base(name), strings.Join(args, " "), err, out)
	}
	return nil
}

// stockEquivalence builds each example binary by the source path with the
// pristine and with the patched compiler (no -ergoir) and compares them
// byte for byte.
func stockEquivalence(r *result, pristine, patched, work, examples string) error {
	var dirs []string
	if examples != "" {
		dirs = strings.Split(examples, ",")
	} else {
		mains, _, err := irspike.ExampleTargets(".")
		if err != nil {
			return err
		}
		dirs = mains
	}
	stock := irspike.Toolchain{GOROOT: pristine, GOCACHE: filepath.Join(work, "gocache-stock")}
	ours := irspike.Toolchain{GOROOT: patched, GOCACHE: filepath.Join(work, "gocache-patched")}
	var errs []error
	for i, dir := range dirs {
		u, err := irspike.Load(dir)
		if err != nil {
			return fmt.Errorf("%s: %w", dir, err)
		}
		mod := filepath.Join(work, "modules", fmt.Sprint(i))
		if _, err := irspike.Emit(u, irspike.Source, mod); err != nil {
			return fmt.Errorf("%s: %w", dir, err)
		}
		var bins [2][]byte
		for j, tc := range []irspike.Toolchain{stock, ours} {
			out := filepath.Join(work, "bin", fmt.Sprint(i, "-", j))
			if _, err := irspike.Build(tc, irspike.Source, mod, out, irspike.BuildOptions{}); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", dir, err))
				break
			}
			if bins[j], err = os.ReadFile(out); err != nil {
				return err
			}
		}
		r.built++
		if bins[0] != nil && bytes.Equal(bins[0], bins[1]) {
			r.equal++
		} else {
			r.differ = append(r.differ, dir)
		}
	}
	if len(r.differ) > 0 {
		errs = append(errs, fmt.Errorf("the patched compiler without -ergoir is not stock on %d of %d examples", len(r.differ), r.built))
	}
	return errors.Join(errs...)
}
