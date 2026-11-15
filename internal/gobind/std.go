package gobind

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// stdSkip are standard-library packages with no binding: they build only
// with cgo or a sanitizer, or they are the language's own (unsafe, builtin).
var stdSkip = []string{"builtin", "unsafe", "runtime/cgo", "runtime/race", "runtime/msan", "runtime/asan"}

// StdPaths lists the import paths of Go's standard library that get a
// binding: every package outside internal and vendor, minus stdSkip.
func StdPaths(env []string) ([]string, error) {
	cmd := exec.Command("go", "list", "std")
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list std: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	var paths []string
	for _, p := range strings.Fields(string(out)) {
		if internalPath(p) || slices.Contains(stdSkip, p) || strings.HasPrefix(p, "cmd/") {
			continue
		}
		paths = append(paths, p)
	}
	return paths, nil
}

// GoVersion is the language version of the go command env runs, go1.N:
// a binding of the standard library is stamped with it, and Go's
// compatibility promise keeps it valid through that version's releases.
func GoVersion(env []string) (string, error) {
	cmd := exec.Command("go", "env", "GOVERSION")
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("go env GOVERSION: %v", err)
	}
	v := regexp.MustCompile(`^go1\.\d+`).FindString(strings.TrimSpace(string(out)))
	if v == "" {
		return "", fmt.Errorf("go env GOVERSION: unexpected %q", out)
	}
	return v, nil
}

// StdBindings writes the binding of every package of StdPaths, by import
// path. dir is any directory: the standard library needs no module.
func StdBindings(dir string, env []string) (map[string]string, error) {
	paths, err := StdPaths(env)
	if err != nil {
		return nil, err
	}
	v, err := GoVersion(env)
	if err != nil {
		return nil, err
	}
	return Bind(dir, env, v, paths...)
}

// FileName is where the binding of path lives under a binding root:
// `net/url/url.ergo`, the folder of its import path and a file named as
// its last element.
func FileName(path string) string {
	return filepath.Join(filepath.FromSlash(path), lastElem(path)+".ergo")
}

// WriteAll writes each binding under root, by FileName.
func WriteAll(root string, files map[string]string) error {
	for path, text := range files {
		name := filepath.Join(root, FileName(path))
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(name, []byte(text), 0o644); err != nil {
			return err
		}
	}
	return nil
}
