package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestGoModuleImports is IG7's acceptance (docs/spec/interop.md, Go
// modules): an Ergo module with a go.mod and go.sum beside ergo.mod imports
// two Go modules at Level 1, one replaced by a local folder
// (example.com/greet) and one required at a version from a module proxy
// (example.com/tally, served by a file:// proxy in the test's folder, so no
// network is reached). `ergo bind go` writes their stamped bindings under
// ergo.bind/, and `ergo run` builds and runs the program. Then, with only a
// stub go on PATH that records any start: a version edit in go.mod makes
// tally's binding stale, a missing binding names the command, and an
// internal package is refused, all at the import and with no Go process
// started. Last, a binding edited to lie about its Go target checks, and
// go build fails at the Ergo line of the call.
//
// Mutants it alone catches: the emitted go.mod drops the replace lines; the
// emitted module drops go.sum; a relative replacement not made absolute;
// the stamp compared without the replacement or against another version;
// ergo check runs a go command on a miss; bind writes no module stamp;
// bind admits an internal package; import go of an internal path asks for
// ergo bind; ergo fmt of a folder rewrites ergo.bind.
//
// Builds Go because module resolution (require, replace, go.sum, the
// proxy) is go build's: no in-process check sees it.
func TestGoModuleImports(t *testing.T) {
	binary := buildTestCLI(t)
	tmp := t.TempDir()
	app := filepath.Join(tmp, "app")
	writeFiles(t, tmp, map[string]string{
		"greet/go.mod": "module example.com/greet\n\ngo 1.27\n",
		"greet/greet.go": `package greet

import "errors"

func Hello(name string) string { return "hello " + name }

type Counter struct{ n int }

func New() *Counter { return &Counter{} }

func (c *Counter) Add(k int) int { c.n += k; return c.n }

var ErrEmpty = errors.New("empty name")

func Must(name string) (string, error) {
	if name == "" {
		return "", ErrEmpty
	}
	return Hello(name), nil
}
`,
		"greet/internal/x/x.go": "package x\n\nfunc X() int { return 1 }\n",
		"app/ergo.mod":          "module app\n",
		"app/cmd/app/main.ergo": `import go example.com/greet
import go example.com/tally
import go strconv

func main() needs Console | go.Foreign {
	Console.Println(greet.Hello("ergo"))
	Console.Println(strconv.Itoa(tally.Sum([]int{1, 2, 3})))
	match greet.New() {
	case Some[go.Ref[greet.Counter]] as c:
		c.Value.Add(2)
		Console.Println(strconv.Itoa(c.Value.Add(5)))
	case None:
		Console.Println("nil")
	}
	let r = greet.Must("") catch {
		case go.Error as e: if go.Is(e, greet.ErrEmpty) { "empty" } else { e.Text }
	}
	Console.Println(r)
}
`,
		"app/cmd/inside/main.ergo": "import go example.com/greet/internal/x\n\nfunc main() {\n}\n",
	})
	tallyMod := "module example.com/tally\n\ngo 1.27\n"
	tallyGo := "package tally\n\nfunc Sum(xs []int) int {\n\tn := 0\n\tfor _, x := range xs {\n\t\tn += x\n\t}\n\treturn n\n}\n"
	proxy := filepath.Join(tmp, "proxy")
	goSum := serveModule(t, proxy, "example.com/tally", "v1.0.0", map[string]string{"go.mod": tallyMod, "tally.go": tallyGo})
	goMod := "module app\n\ngo 1.27\n\nrequire (\n\texample.com/greet v0.0.0\n\texample.com/tally v1.0.0\n)\n\nreplace example.com/greet => ../greet\n"
	writeFiles(t, app, map[string]string{"go.mod": goMod, "go.sum": goSum})

	env := goModuleEnv(tmp, proxy)
	cli := func(env []string, args ...string) (string, int) {
		t.Helper()
		command := commandIn(binary, app, env, args...)
		stdout, stderr, code := runSmokeCommand(t, command)
		return string(stdout) + string(stderr), code
	}

	if out, code := cli(env, "bind", "go", "example.com/greet", "example.com/tally"); code != 0 {
		t.Fatalf("ergo bind go: exit %d\n%s", code, out)
	}
	greetBinding := filepath.Join(app, "ergo.bind", "example.com", "greet", "greet.ergo")
	tallyBinding := filepath.Join(app, "ergo.bind", "example.com", "tally", "tally.ergo")
	for file, stamp := range map[string]string{
		greetBinding: "// ergo:module example.com/greet v0.0.0 => ../greet\n",
		tallyBinding: "// ergo:module example.com/tally v1.0.0\n",
	} {
		if text := readText(t, file); !strings.Contains(text, stamp) {
			t.Fatalf("%s lacks the stamp %q:\n%s", file, stamp, firstLines([]byte(text)))
		}
	}
	if out, code := cli(env, "bind", "go", "example.com/greet/internal/x"); code == 0 || !strings.Contains(out, "internal or vendor package only inside the tree") {
		t.Errorf("ergo bind go of an internal package: exit %d\n%s", code, out)
	}
	if out, code := cli(env, "run", "cmd/app"); code != 0 || !strings.HasSuffix(out, "hello ergo\n6\n7\nempty\n") {
		t.Fatalf("ergo run cmd/app: exit %d\n%s", code, out)
	}

	// The check reads files only: a go on PATH that ran would leave a mark.
	stubDir := filepath.Join(tmp, "stub")
	mark := filepath.Join(tmp, "go-started")
	writeFiles(t, stubDir, map[string]string{"go": "#!/bin/sh\n: > " + mark + "\nexit 1\n"})
	if err := os.Chmod(filepath.Join(stubDir, "go"), 0o755); err != nil {
		t.Fatal(err)
	}
	stubEnv := append(slices.DeleteFunc(slices.Clone(env), func(kv string) bool { return strings.HasPrefix(kv, "PATH=") }), "PATH="+stubDir)
	writeFiles(t, app, map[string]string{"go.mod": strings.Replace(goMod, "tally v1.0.0", "tally v1.1.0", 1)})
	saved := readText(t, greetBinding)
	if err := os.Remove(greetBinding); err != nil {
		t.Fatal(err)
	}
	out, code := cli(stubEnv, "diagnostics", "cmd/app")
	for _, want := range []string{
		`cmd/app/main.ergo:1:11: error S104: no binding for Go package "example.com/greet"; run ergo bind go example.com/greet`,
		`cmd/app/main.ergo:2:11: error S104: the binding of Go package "example.com/tally" is stale: it was made for example.com/tally v1.0.0, and go.mod now names example.com/tally v1.1.0; run ergo bind go example.com/tally`,
	} {
		if code == 0 || !strings.Contains(out, want) {
			t.Errorf("ergo diagnostics after the go.mod edit: exit %d, want %q in\n%s", code, want, out)
		}
	}
	out, code = cli(stubEnv, "diagnostics", "cmd/inside")
	if want := `cmd/inside/main.ergo:1:11: error S109: import go "example.com/greet/internal/x": Go admits an internal package only inside the tree that holds it`; code == 0 || !strings.Contains(out, want) {
		t.Errorf("ergo diagnostics of an internal import: exit %d, want %q in\n%s", code, want, out)
	}
	if _, err := os.Stat(mark); err == nil {
		t.Error("ergo check started a go command")
	}

	// A binding that lies about its Go target checks; go build refuses
	// the call at its Ergo line.
	writeFiles(t, app, map[string]string{"go.mod": goMod})
	lie := strings.Replace(saved, "extern go example.com/greet.Hello ", "extern go example.com/greet.Howdy ", 1)
	if lie == saved {
		t.Fatalf("the greet binding binds no Hello:\n%s", saved)
	}
	// Two more blank lines: ergo fmt would rewrite the binding, but a fmt
	// of the module's folder skips ergo.bind.
	lie = strings.Replace(lie, "// ergo:go func Hello", "\n\n// ergo:go func Hello", 1)
	writeFiles(t, filepath.Dir(greetBinding), map[string]string{"greet.ergo": lie})
	if out, code := cli(env, "fmt", "."); code != 0 || readText(t, greetBinding) != lie {
		t.Errorf("ergo fmt of the module: exit %d, or it rewrote ergo.bind\n%s", code, out)
	}
	out, code = cli(env, "run", "cmd/app")
	if code == 0 || !strings.Contains(out, "cmd/app/main.ergo:6:") || !strings.Contains(out, "Howdy") {
		t.Errorf("ergo run with a lying binding: exit %d, want a go build error at cmd/app/main.ergo:6\n%s", code, out)
	}
}

// goModuleEnv is the environment of the module test: the test's own Go
// module cache (writable, so the folder can be removed), its file proxy,
// no checksum database, no workspace, and its own emission cache.
func goModuleEnv(tmp, proxy string) []string {
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		for _, k := range []string{"GOFLAGS=", "GOPROXY=", "GOSUMDB=", "GONOSUMDB=", "GOPRIVATE=", "GONOPROXY=", "GOMODCACHE=", "GOWORK=", "XDG_CACHE_HOME="} {
			if strings.HasPrefix(kv, k) {
				return true
			}
		}
		return false
	})
	return append(env, "GOFLAGS=-modcacherw", "GOPROXY=file://"+filepath.ToSlash(proxy), "GOSUMDB=off", "GOWORK=off",
		"GOMODCACHE="+filepath.Join(tmp, "modcache"), "XDG_CACHE_HOME="+filepath.Join(tmp, "cache"))
}

// serveModule writes module path@version with files into a GOPROXY folder
// and returns its go.sum lines, hashed as the go command hashes (h1).
func serveModule(t *testing.T, proxy, path, version string, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(proxy, filepath.FromSlash(path), "@v")
	var zipped bytes.Buffer
	w := zip.NewWriter(&zipped)
	inZip := map[string]string{}
	for name, text := range files {
		full := path + "@" + version + "/" + name
		inZip[full] = text
		f, err := w.Create(full)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(text)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, dir, map[string]string{
		"list":            version + "\n",
		version + ".info": `{"Version":"` + version + `","Time":"2026-01-01T00:00:00Z"}`,
		version + ".mod":  files["go.mod"],
		version + ".zip":  zipped.String(),
	})
	return fmt.Sprintf("%s %s %s\n%s %s/go.mod %s\n", path, version, hash1(inZip), path, version, hash1(map[string]string{"go.mod": files["go.mod"]}))
}

// hash1 is the go command's h1 hash of a file tree (dirhash.Hash1).
func hash1(files map[string]string) string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	var summary strings.Builder
	for _, name := range names {
		fmt.Fprintf(&summary, "%x  %s\n", sha256.Sum256([]byte(files[name])), name)
	}
	sum := sha256.Sum256([]byte(summary.String()))
	return "h1:" + base64.StdEncoding.EncodeToString(sum[:])
}

// commandIn is the CLI run in dir with exactly env.
func commandIn(binary, dir string, env []string, args ...string) *exec.Cmd {
	command := exec.Command(binary, args...)
	command.Dir, command.Env = dir, env
	return command
}

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, text := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func readText(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
