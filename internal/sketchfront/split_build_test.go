package sketchfront

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"ergo.local/prototype/internal/sketch/build"
)

// The counted tests of the package split (compile-speed CS4): what the go
// command compiles when one Ergo package changes, and how many program
// packages it compiles at once. Both build Go because the question is what
// the go command does with the emitted module; no in-process check sees
// its actions. They count events (the go command's -x lines, a toolexec
// wrapper's calls), never time.

// layeredProgram is a module of width leaf packages l0 to l<width-1>, a
// package mid over all of them, and the root package app over mid and
// the leaves. Each leaf's Work is too large for Ergo's inliner and Go's,
// so its body is no importer's. nonce goes into every leaf, so no leaf is
// in Go's build cache; k is the literal a body edit changes.
func layeredProgram(width int, nonce string, k int) map[string]string {
	files := map[string]string{"ergo.mod": "module layered\n"}
	var imports, calls []string
	for i := range width {
		name := fmt.Sprintf("l%d", i)
		var b strings.Builder
		fmt.Fprintf(&b, "func Nonce() string { %q }\n\n", nonce)
		b.WriteString("func Work(n int) int {\n\tlet mut t = 0\n\tfor j in 0..n {\n")
		lit := 3 + i
		if i == 0 {
			lit = k
		}
		fmt.Fprintf(&b, "\t\tt = t + (j * %d) %% 13\n", lit)
		for s := range 24 {
			fmt.Fprintf(&b, "\t\tif t %% %d == %d {\n\t\t\tt = t + j * %d - %d\n\t\t}\n", s+2, s%2, s+1, s)
		}
		b.WriteString("\t}\n\tt\n}\n")
		files[name+"/"+name+".ergo"] = b.String()
		imports = append(imports, "import layered/"+name)
		calls = append(calls, name+".Work(j)")
	}
	var mid strings.Builder
	mid.WriteString(strings.Join(imports, "\n") + "\n\nfunc Sum(n int) int {\n\tlet mut t = 0\n\tfor j in 0..n {\n\t\tt = t + " + strings.Join(calls, " + ") + "\n")
	for s := range 12 {
		fmt.Fprintf(&mid, "\t\tif t %% %d == 1 {\n\t\t\tt = t - %d\n\t\t}\n", s+2, s)
	}
	mid.WriteString("\t}\n\tt\n}\n")
	files["mid/mid.ergo"] = mid.String()
	files["app/main.ergo"] = "import layered/l0\nimport layered/mid\n\nfunc main() needs Console {\n\tConsole.Println(\"\\(mid.Sum(30)) \\(l0.Work(5)) \\(l0.Nonce())\")\n}\n"
	return files
}

// writeFiles writes files below dir.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, text := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// buildDir builds the package of the folder dir with GOFLAGS flags, and
// returns the go command's output.
func buildDir(t *testing.T, cache, dir, output, flags string) string {
	t.Helper()
	analysis, err := CheckDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(analysis.Diagnostics) > 0 {
		t.Fatalf("diagnostics: %+v", analysis.Diagnostics)
	}
	tc := build.HostToolchain()
	tc.GOFLAGS = strings.TrimSpace(tc.GOFLAGS + " " + flags)
	var stderr bytes.Buffer
	req := BuildRequest{Mode: build.ModeBuild, Source: filepath.Join(dir, "main.ergo"), Output: output, Cache: build.Cache{Root: cache}, Toolchain: tc, Stderr: &stderr}
	if _, err := Build(analysis, req); err != nil {
		t.Fatalf("build: %v\n%s", err, stderr.String())
	}
	return stderr.String()
}

// goActions lists the program's own packages the go command compiled, and
// counts its links, from its -x output.
func goActions(x string) (compiles []string, links int) {
	for _, line := range strings.Split(x, "\n") {
		f := strings.Fields(line)
		for len(f) > 0 && strings.Contains(f[0], "=") {
			f = f[1:] // an environment assignment
		}
		if len(f) == 0 {
			continue
		}
		switch filepath.Base(f[0]) {
		case "compile":
			for i := 0; i+1 < len(f); i++ {
				if f[i] == "-p" && (f[i+1] == "main" || strings.HasPrefix(f[i+1], "ergo.local/program")) {
					compiles = append(compiles, f[i+1])
				}
			}
		case "link":
			links++
		}
	}
	return compiles, links
}

func newNonce(t *testing.T) string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// TestBodyEditCompilesOnePackage: after a one-line body edit in the
// lowest package of a layered program, the go command compiles that
// package and the packages that import it (mid and the root), and links
// once: the leaves l1 and l2 and the shared package are not compiled
// again. The one-package reference compiles the whole program again (its
// one package, main). The go command keys an import by the content of its
// whole archive, not by its export data (cmd/go buildActionID), so the
// importers of an edited package compile again even when its export data
// is the same; one compile per body edit needs a driver that keys on
// export data (compile-speed CS10, BUILD-direct).
func TestBodyEditCompilesOnePackage(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
	const width = 3
	nonce := newNonce(t)
	dir := t.TempDir()
	cache := t.TempDir()
	app := filepath.Join(dir, "app")
	writeFiles(t, dir, layeredProgram(width, nonce, 17))
	first := buildDir(t, cache, app, filepath.Join(t.TempDir(), "app"), "-x")
	if c, l := goActions(first); len(c) < width+1 || l != 1 {
		t.Fatalf("first build: compiled %v and linked %d times, want at least the %d leaves and the root, and 1 link", c, l, width)
	}
	writeFiles(t, dir, layeredProgram(width, nonce, 19))
	second := buildDir(t, cache, app, filepath.Join(t.TempDir(), "app"), "-x")
	want := []string{"ergo.local/program/pkg/layered/l0", "ergo.local/program/pkg/layered/mid", "main"}
	if c, l := goActions(second); !slices.Equal(c, want) || l != 1 {
		t.Fatalf("after a body edit of l0: compiled %v and linked %d times, want %v and 1 link", c, l, want)
	}
	// The reference, selected by ERGO_LOWER=single, is a module of its own:
	// it compiles the whole program as one package.
	t.Setenv(LowerSingleEnv, "single")
	single := buildDir(t, cache, app, filepath.Join(t.TempDir(), "app"), "-x")
	if c, l := goActions(single); !slices.Equal(c, []string{"main"}) || l != 1 {
		t.Fatalf("the one-package reference compiled %v and linked %d times, want [main] and 1 link", c, l)
	}
}

// wrapperSource is a -toolexec program: before a compile of a package
// that SPLIT_HOLD lists, it tells the test's socket the package and waits
// for one byte; then it runs the tool.
const wrapperSource = `package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
)

func main() {
	tool, args := os.Args[1], os.Args[2:]
	pkg := ""
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-p" {
			pkg = args[i+1]
		}
	}
	if strings.HasSuffix(tool, "compile") && pkg != "" && strings.Contains(" "+os.Getenv("SPLIT_HOLD")+" ", " "+pkg+" ") {
		conn, err := net.Dial("unix", os.Getenv("SPLIT_SOCKET"))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(3)
		}
		fmt.Fprintln(conn, pkg)
		var b [1]byte
		conn.Read(b[:])
		conn.Close()
	}
	cmd := exec.Command(tool, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			os.Exit(e.ExitCode())
		}
		os.Exit(4)
	}
}
`

// TestColdCompilesInParallel: with a toolchain whose compile of each leaf
// package waits until the test releases it, the leaves of a layered
// program, which import nothing of each other, are all in flight at once:
// the in-flight count reaches the import graph's width. The test releases
// them when it does; a build that compiles them one at a time never gets
// there, and the test's deadline stops it and names the count.
func TestColdCompilesInParallel(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
	const width = 3
	tmp := t.TempDir()
	writeFiles(t, filepath.Join(tmp, "wrap"), map[string]string{"go.mod": "module wrap\n\ngo 1.27\n", "main.go": wrapperSource})
	wrapper := filepath.Join(tmp, "wrapper")
	cmd := exec.Command("go", "build", "-o", wrapper, ".")
	cmd.Dir = filepath.Join(tmp, "wrap")
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the wrapper does not build: %v\n%s", err, out)
	}
	socket := filepath.Join(tmp, "s")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var hold []string
	for i := range width {
		hold = append(hold, fmt.Sprintf("ergo.local/program/pkg/layered/l%d", i))
	}
	t.Setenv("SPLIT_SOCKET", socket)
	t.Setenv("SPLIT_HOLD", strings.Join(hold, " "))

	var mu sync.Mutex
	var held []net.Conn
	high, released := 0, false
	release := func() {
		released = true
		for _, c := range held {
			c.Write([]byte{1})
		}
		held = nil
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			if _, err := bufio.NewReader(c).ReadString('\n'); err != nil {
				c.Close()
				continue
			}
			mu.Lock()
			held = append(held, c)
			high = max(high, len(held))
			if released || len(held) == width {
				release()
			}
			mu.Unlock()
		}
	}()
	if deadline, ok := t.Deadline(); ok {
		// Only a hung build reaches this: it stops the test and names it.
		stop := time.AfterFunc(time.Until(deadline)-time.Minute, func() {
			mu.Lock()
			defer mu.Unlock()
			t.Errorf("hung: %d of the %d leaf compiles were in flight at once", high, width)
			release()
		})
		defer stop.Stop()
	}
	dir := filepath.Join(tmp, "src")
	writeFiles(t, dir, layeredProgram(width, newNonce(t), 17))
	buildDir(t, t.TempDir(), filepath.Join(dir, "app"), filepath.Join(tmp, "app"), fmt.Sprintf("-p=%d -toolexec=%s", width+1, wrapper))
	mu.Lock()
	defer mu.Unlock()
	if high != width {
		t.Fatalf("at most %d leaf compiles were in flight at once, want the graph's width %d", high, width)
	}
}
