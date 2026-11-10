package runtimefiles

import (
	"bytes"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestPackagesMatchTheirDirectories: each runtime package's list of files is
// every non-test file of its directory except runtime/v1/files.go, which
// serves only the compiler; each embedded file has the bytes on disk; and
// every runtime package a file imports is in the package's declared
// imports, so Files never leaves an import of an emitted module unmet.
func TestPackagesMatchTheirDirectories(t *testing.T) {
	for path, p := range packages {
		dir := strings.TrimPrefix(path, "runtime/")
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		var onDisk []string
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || path+"/"+name == "runtime/v1/files.go" {
				continue
			}
			onDisk = append(onDisk, name)
		}
		if !slices.Equal(onDisk, p.files) {
			t.Errorf("%s: files %v; the directory has %v", path, p.files, onDisk)
		}
		for _, name := range p.files {
			disk, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			embedded, err := sources.ReadFile(dir + "/" + name)
			if err != nil || !bytes.Equal(disk, embedded) {
				t.Errorf("%s/%s: the embedded bytes differ from the file (%v)", path, name, err)
			}
			file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), disk, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, spec := range file.Imports {
				imported, _ := strconv.Unquote(spec.Path.Value)
				rel, ok := strings.CutPrefix(imported, Module+"/")
				if !ok {
					continue
				}
				if !slices.Contains(p.imports, rel) {
					t.Errorf("%s/%s imports %s, which the package does not declare", path, name, rel)
				}
			}
		}
	}
	if _, err := Files("runtime/nope"); err == nil {
		t.Error("Files accepted a package that is not a runtime package")
	}
}

// program is a lowered program, written by hand as the lowering will emit
// it once the std calls lower (check-v1 stage D): a frame with an exit
// action starts a producer task that pushes into a queue and closes it; the
// frame drains the queue with Values and waits for the task.
const program = `package main

import (
	"fmt"

	"ergo.local/prototype/runtime/rt"
	ergo "ergo.local/prototype/runtime/v1"
)

func run(t *rt.Task) (err error) {
	s := ergo.NewScope(t)
	defer s.End(&err)
	q := ergo.New[int](2)
	ergo.OnExit(&s, func() { fmt.Println("exit action") })
	producer := ergo.Go(&s, "producer at main.ergo:5:14", func(pt *rt.Task) (int, error) {
		for i := 1; i <= 3; i++ {
			if err := q.Push(pt, i); err != nil {
				return 0, err
			}
		}
		q.CloseSend()
		return 3, nil
	})
	sum := 0
	for v := range q.Values(t) {
		sum += v
	}
	n, err := producer.Wait()
	if err != nil {
		return err
	}
	fmt.Println(n, sum)
	return nil
}

func main() { ergo.Main(run) }
`

// TestEmittedTreeBuildsAlone (stage 3b-runtime item 1): a module laid out as
// the lowering lays one out (the program at the root, the runtime module in
// ergo/ with the files Files gives for runtime/v1) builds, passes go vet
// and runs, from that tree alone: no module download (GOPROXY=off) and no
// file of this repository.
func TestEmittedTreeBuildsAlone(t *testing.T) {
	files, err := Files("runtime/v1")
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	if !slices.Contains(paths, "runtime/rt/task.go") || !slices.Contains(paths, "runtime/v1/queue.go") || slices.Contains(paths, "runtime/v1/files.go") {
		t.Fatalf("Files(runtime/v1) = %v; want runtime/v1 and runtime/rt, without files.go", paths)
	}
	dir := t.TempDir()
	write := func(path string, data []byte) {
		t.Helper()
		full := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", []byte("module ergo.local/program\n\ngo 1.27\n\nrequire "+Module+" v0.0.0\n\nreplace "+Module+" => ./ergo\n"))
	write("main.go", []byte(program))
	write("ergo/go.mod", []byte("module "+Module+"\n\ngo 1.27\n"))
	for _, f := range files {
		write("ergo/"+f.Path, f.Data)
	}
	env := append(os.Environ(), "GOFLAGS=", "GOWORK=off", "GOPROXY=off", "CGO_ENABLED=0")
	gocmd := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("go", args...)
		cmd.Dir, cmd.Env = dir, env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %s in the emitted tree: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	gocmd("build", "-o", "program", ".")
	gocmd("vet", "./...")
	out, err := exec.Command(filepath.Join(dir, "program")).Output()
	if err != nil {
		t.Fatalf("the program: %v", err)
	}
	if string(out) != "3 6\nexit action\n" {
		t.Fatalf("the program printed %q", out)
	}
}
