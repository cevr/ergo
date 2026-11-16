package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"ergo.local/prototype/internal/sketchfront"
)

const getHelp = "usage: ergo get MODULE[@VERSION]...\n\n" +
	"Requires each module in the Ergo module at or above the current folder\n" +
	"(docs/spec/grammar.md, Source files 1, Required modules). It runs go get in\n" +
	"the module root: go.mod pins the module, go.sum holds its hashes, and Go's\n" +
	"module cache holds its copy. GOPROXY, GOPRIVATE, GONOSUMDB, GOFLAGS and the\n" +
	"checksum database work as they do for go get. A VERSION is any query go get\n" +
	"takes; none means the latest version.\n\n" +
	"Then it reads each module as an import of its packages does: the cached\n" +
	"copy must match go.sum, and the ergo.mod of an Ergo module must name the\n" +
	"module's path. When a module fails, go.mod and go.sum are put back as they\n" +
	"were. A module with no ergo.mod is a Go module: it is required, and Ergo\n" +
	"imports its packages through ergo bind go. With no go.mod beside ergo.mod,\n" +
	"ergo get runs go mod init first.\n\n" +
	"Exit codes: 0 required; 1 go failed or a module cannot be imported;\n" +
	"2 invalid invocation.\n"

// getCommand is `ergo get MODULE[@VERSION]...` (MD2).
func getCommand(args []string) error {
	if len(args) == 0 {
		return usageError{message: "usage: ergo get MODULE[@VERSION]..."}
	}
	var paths []string
	for _, arg := range args {
		path, version, _ := strings.Cut(arg, "@")
		switch {
		case path == "" || strings.HasPrefix(arg, "-"):
			return usageError{message: fmt.Sprintf("ergo get: %q is not MODULE[@VERSION]", arg)}
		case version == "none":
			return usageError{message: fmt.Sprintf("ergo get adds a requirement; go get %s removes one", arg)}
		}
		paths = append(paths, path)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return exitStatusError{code: 1, message: "ergo get: " + err.Error()}
	}
	root, name, err := sketchfront.FindModule(cwd)
	if err != nil {
		return exitStatusError{code: 1, message: "ergo get: " + err.Error()}
	}
	if err := useGoCommand(); err != nil {
		return exitStatusError{code: 1, message: "ergo get: " + err.Error()}
	}
	kept, err := keepFiles(root, sketchfront.GoModFile, sketchfront.GoSumFile)
	if err != nil {
		return exitStatusError{code: 1, message: "ergo get: " + err.Error()}
	}
	fail := func(message string) error {
		if err := kept.restore(); err != nil {
			message += "; go.mod and go.sum could not be put back: " + err.Error()
		} else {
			message += "; go.mod and go.sum are as they were"
		}
		return exitStatusError{code: 1, message: "ergo get: " + message}
	}
	if kept.files[sketchfront.GoModFile] == nil {
		if err := goIn(root, "mod", "init", name); err != nil {
			return fail(err.Error())
		}
	}
	if err := goIn(root, append([]string{"get"}, args...)...); err != nil {
		return fail(err.Error())
	}
	var lines []string
	for _, path := range paths {
		r, err := sketchfront.Required(root, path)
		switch {
		case err != nil:
			return fail(err.Error())
		case r.GoModule:
			lines = append(lines, fmt.Sprintf("required %s %s, a Go module: ergo bind go binds its packages", r.Path, r.Version))
		case r.Why != "":
			return fail(fmt.Sprintf("%s: %s", path, r.Why))
		default:
			lines = append(lines, fmt.Sprintf("required %s %s", r.Path, r.Version))
		}
	}
	for _, line := range lines {
		fmt.Println(line)
	}
	return nil
}

// goIn runs the go command in dir, its output on ergo's stderr.
func goIn(dir string, args ...string) error {
	command := exec.Command("go", args...)
	command.Dir, command.Stdout, command.Stderr = dir, os.Stderr, os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("go %s: %v", strings.Join(args, " "), err)
	}
	return nil
}

// keptFiles are files of the folder dir as they were, by name; nil for
// one that did not exist.
type keptFiles struct {
	dir   string
	files map[string][]byte
}

func keepFiles(dir string, names ...string) (*keptFiles, error) {
	kept := &keptFiles{dir: dir, files: map[string][]byte{}}
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		switch {
		case err == nil:
			kept.files[name] = data
		case errors.Is(err, fs.ErrNotExist):
			kept.files[name] = nil
		default:
			return nil, err
		}
	}
	return kept, nil
}

// restore writes each kept file back, and removes one that did not exist.
func (k *keptFiles) restore() error {
	var errs []error
	for name, data := range k.files {
		path := filepath.Join(k.dir, name)
		if data == nil {
			if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, err)
			}
			continue
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
