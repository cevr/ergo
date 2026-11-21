package task

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// GoGen is a generator ergo.work declares: `gen NAME COMMAND...` and its
// `reads NAME PATH...` lines.
type GoGen struct {
	Name    string
	Command []string
	Reads   []string
}

// goPackage is the package a gen's go command builds: its one argument
// that names a folder ("./DIR" or "."), and whether the command is go
// test, whose build holds the package's test files too.
func goPackage(g GoGen) (pkg string, test bool, err error) {
	if len(g.Command) < 3 || g.Command[0] != "go" || (g.Command[1] != "run" && g.Command[1] != "test") {
		return "", false, fmt.Errorf("gen %s: the command is go run PKG ... or go test ... PKG ...; another command is a foreign task (WT2)", g.Name)
	}
	for _, w := range g.Command[2:] {
		if w == "." || strings.HasPrefix(w, "./") {
			if pkg != "" {
				return "", false, fmt.Errorf("gen %s: the command names two packages, %s and %s; a gen builds one", g.Name, pkg, w)
			}
			pkg = w
		}
	}
	if pkg == "" {
		return "", false, fmt.Errorf("gen %s: name the command's package as ./DIR", g.Name)
	}
	return pkg, g.Command[1] == "test", nil
}

// GoEnv is the Go toolchain's environment a go command's build depends
// on, read once per process by `go env`.
var GoEnv = sync.OnceValues(func() (string, error) {
	out, err := exec.Command("go", "env", "GOVERSION", "GOOS", "GOARCH", "GOFLAGS", "CGO_ENABLED", "GOEXPERIMENT", "GOAMD64", "GOARM64").Output()
	if err != nil {
		return "", fmt.Errorf("go env: %v", err)
	}
	return string(out), nil
})

// goListTemplate prints, per non-standard package, "P DIR FILE..." with
// every file its build reads, its tests' too, and "M GOMOD" for its
// module's go.mod.
const goListTemplate = `{{if not .Standard}}P {{.Dir}}{{range .GoFiles}} {{.}}{{end}}{{range .CgoFiles}} {{.}}{{end}}{{range .SFiles}} {{.}}{{end}}{{range .HFiles}} {{.}}{{end}}{{range .EmbedFiles}} {{.}}{{end}}{{range .TestGoFiles}} {{.}}{{end}}{{range .XTestGoFiles}} {{.}}{{end}}{{range .TestEmbedFiles}} {{.}}{{end}}{{range .XTestEmbedFiles}} {{.}}{{end}}{{if .Module}}{{if .Module.GoMod}}
M {{.Module.GoMod}}{{end}}{{end}}{{end}}`

// goClosure is the build inputs of package pkg in the module at root:
// every file of every package its build (with test, its test build)
// reads below root, and each such package's folder and the folder of
// each embedded file, by listing. Files outside root (the module cache,
// GOROOT) are left out: go.sum and the Go version key them.
func goClosure(ctx context.Context, root, pkg string, test bool) (Inputs, error) {
	args := []string{"list", "-deps", "-f", goListTemplate}
	if test {
		args = append(args, "-test")
	}
	cmd := exec.CommandContext(ctx, "go", append(args, pkg)...)
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return Inputs{}, fmt.Errorf("go list %s: %v: %s", pkg, err, strings.TrimSpace(stderr.String()))
	}
	var in Inputs
	rel := func(abs string) (string, bool) {
		r, err := filepath.Rel(root, abs)
		if err != nil || !filepath.IsLocal(r) {
			return "", false
		}
		return filepath.ToSlash(r), true
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(nil, 1<<24)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "M":
			if r, ok := rel(fields[1]); ok {
				in.Files = append(in.Files, r)
				in.Files = append(in.Files, strings.TrimSuffix(r, "go.mod")+"go.sum")
			}
		case "P":
			dir, ok := rel(fields[1])
			if !ok {
				continue
			}
			in.Dirs = append(in.Dirs, dir)
			for _, f := range fields[2:] {
				p := filepath.ToSlash(filepath.Join(dir, f))
				in.Files = append(in.Files, p)
				in.Dirs = append(in.Dirs, parentDir(p))
			}
		}
	}
	in.Files = append(in.Files, "go.mod", "go.sum")
	return in, nil
}

// readsOf is the inputs of declared reads below root: a file by content,
// a folder by every file and folder below it, and a path that does not
// exist as both (so that it appearing, as either, is a miss).
func readsOf(root string, reads []string) (Inputs, error) {
	var in Inputs
	for _, p := range reads {
		abs := filepath.Join(root, filepath.FromSlash(p))
		info, err := os.Stat(abs)
		if errors.Is(err, fs.ErrNotExist) {
			in.Files = append(in.Files, p)
			in.Dirs = append(in.Dirs, p, parentDir(p))
			continue
		}
		if err != nil {
			return Inputs{}, err
		}
		if !info.IsDir() {
			in.Files = append(in.Files, p)
			in.Dirs = append(in.Dirs, parentDir(p))
			continue
		}
		err = filepath.WalkDir(abs, func(q string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			r, err := filepath.Rel(root, q)
			if err != nil {
				return err
			}
			r = filepath.ToSlash(r)
			if d.IsDir() {
				if d.Name() == ".git" {
					return filepath.SkipDir
				}
				in.Dirs = append(in.Dirs, r)
				return nil
			}
			in.Files = append(in.Files, r)
			return nil
		})
		if err != nil {
			return Inputs{}, err
		}
	}
	return in, nil
}

// GoNode is the node of a declared go generator. Its runs get env added
// to the process environment (the budget's GOMAXPROCS and lent grant).
func GoNode(g GoGen, env []string) (Node, error) {
	pkg, test, err := goPackage(g)
	if err != nil {
		return Node{}, err
	}
	return Node{
		Name:  g.Name,
		Class: Vouched,
		Ident: strings.Join(g.Command, "\x00") + "\x01" + strings.Join(g.Reads, "\x00"),
		Env:   func(context.Context) (string, error) { return GoEnv() },
		Inputs: func(ctx context.Context, root string) (Inputs, error) {
			in, err := goClosure(ctx, root, pkg, test)
			if err != nil {
				return Inputs{}, err
			}
			reads, err := readsOf(root, g.Reads)
			if err != nil {
				return Inputs{}, err
			}
			in.Files = append(in.Files, reads.Files...)
			in.Dirs = append(in.Dirs, reads.Dirs...)
			return in, nil
		},
		Run: func(ctx context.Context, root string) ([]byte, error) {
			cmd := exec.CommandContext(ctx, "go", g.Command[1:]...)
			cmd.Dir = root
			cmd.Env = append(os.Environ(), env...)
			var out bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &out
			ownGroup(cmd)
			cmd.Cancel = func() error { killGroup(cmd); return nil }
			err := cmd.Run()
			killGroup(cmd)
			return out.Bytes(), err
		},
	}, nil
}
