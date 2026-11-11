package selfhostcheck

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/sketchfront"
)

// needsBuild marks a test that builds or runs a Go program: the fast tier
// (-short) skips it, the full tier runs it, as lower's needsBuild does.
func needsBuild(t testing.TB) {
	t.Helper()
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
}

// repoRoot is the repository root, found from this file's location.
func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// pilot is the pilot's Ergo module: ergo.mod, go.mod, the Go adapter and
// the Ergo packages.
func pilot() string {
	return filepath.Join(repoRoot(), "internal", "selfhostcheck", "testdata", "selfhost")
}

// buildDriver lowers the Ergo package pilot()/lib with `ergo lib`'s
// lowering (sketchfront.Library) into a Go module named lib, and builds the
// comparison driver of testdata/driver against it, once. The driver's go.mod
// is the main module, so it alone decides the replacements: the repository
// for ergo.local/prototype (the runtime the library calls and the parser),
// the pilot for ergo.local/prototype/selfhost, the library for lib.
func buildDriver(t *testing.T, lib string) string {
	t.Helper()
	dir := filepath.Join(pilot(), lib)
	a, err := sketchfront.CheckDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Diagnostics) > 0 {
		var ds []string
		for _, d := range a.Diagnostics {
			ds = append(ds, fmt.Sprintf("%s:%d:%d: %s %s", d.File, d.Span.Line, d.Span.Column, d.Code, d.Message))
		}
		t.Fatalf("%s does not check:\n%s", dir, strings.Join(ds, "\n"))
	}
	files, err := sketchfront.Library(a, dir, lib, lib)
	if err != nil {
		t.Fatalf("ergo lib %s: %v", dir, err)
	}
	work := t.TempDir()
	write := func(rel string, data []byte) {
		p := filepath.Join(work, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range files {
		write(lib+"/"+f.Path, f.Data)
	}
	main, err := os.ReadFile(filepath.Join(repoRoot(), "internal", "selfhostcheck", "testdata", "driver", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	write("driver/main.go", main)
	write("driver/go.mod", []byte("module ergo.local/prototype/selfhostprobe\n\ngo 1.27\n\n"+
		"require (\n\t"+lib+" v0.0.0\n\tergo.local/prototype v0.0.0\n\tergo.local/prototype/selfhost v0.0.0\n)\n\n"+
		"replace "+lib+" => "+filepath.Join(work, lib)+"\n\n"+
		"replace ergo.local/prototype => "+repoRoot()+"\n\n"+
		"replace ergo.local/prototype/selfhost => "+pilot()+"\n"))
	exe := filepath.Join(work, "driver.exe")
	// The deadline only stops a hung build and names it.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-o", exe, ".")
	build.Dir = filepath.Join(work, "driver")
	build.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOPROXY=off", "GOWORK=off")
	build.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	build.Cancel = func() error { return syscall.Kill(-build.Process.Pid, syscall.SIGKILL) }
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build of the driver: %v\n%s", err, out)
	}
	return exe
}

// pair is the driver's answer for one source: the projection Go built
// from the parser's tree, and the one the Ergo library built.
type pair struct{ goSide, ergoSide string }

// runDriver runs the driver once over every source, under the program
// memory limit, in its own process group.
func runDriver(t *testing.T, exe string, srcs []string) []pair {
	t.Helper()
	var in bytes.Buffer
	for _, s := range srcs {
		fmt.Fprintf(&in, "%d:%s", len(s), s)
	}
	limit, err := proclimit.Limit(os.Environ(), proclimit.DefaultLimit)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe)
	var stdout, stderr bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = &in, &stdout, &stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := proclimit.Run(cmd, limit); err != nil {
		t.Fatalf("driver: %v\n%s", err, stderr.String())
	}
	r := bufio.NewReader(&stdout)
	var out []pair
	for range srcs {
		g, err1 := readFrame(r)
		e, err2 := readFrame(r)
		if err1 != nil || err2 != nil {
			t.Fatalf("driver answered %d of %d sources: %v %v\n%s", len(out), len(srcs), err1, err2, stderr.String())
		}
		out = append(out, pair{g, e})
	}
	if rest, _ := io.ReadAll(r); len(rest) > 0 {
		t.Fatalf("driver wrote %d bytes past its last answer", len(rest))
	}
	return out
}

func readFrame(r *bufio.Reader) (string, error) {
	head, err := r.ReadString(':')
	if err != nil {
		return "", err
	}
	n, err := strconv.Atoi(strings.TrimSuffix(head, ":"))
	if err != nil {
		return "", err
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return string(b), nil
}
