package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// listedPackage is what driverPackages reads of `go list -deps -json`.
type listedPackage struct {
	ImportPath                                                       string
	Dir                                                              string
	Standard                                                         bool
	Imports                                                          []string
	Module                                                           *struct{ Path, Dir string }
	GoFiles, CgoFiles, CFiles, HFiles, SFiles, SysoFiles, EmbedFiles []string
}

// driverPackages are the module packages of root that driverRoots reach
// without passing through an emissionRoots package, as `go list -deps`
// lists them: the code that drives a program's legs besides the Go the
// compiler emits for it (cache.go, early cutoff).
func driverPackages(root string) ([]listedPackage, error) {
	query := exec.Command("go", append([]string{"list", "-deps", "-json"}, driverRoots...)...)
	query.Dir = root
	var stderr bytes.Buffer
	query.Stderr = &stderr
	out, err := query.Output()
	if err != nil {
		return nil, fmt.Errorf("go list %s: %v: %s", strings.Join(driverRoots, " "), err, strings.TrimSpace(stderr.String()))
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	byPath := map[string]listedPackage{}
	var module string
	decoder := json.NewDecoder(bytes.NewReader(out))
	for {
		var p listedPackage
		if err := decoder.Decode(&p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, err
		}
		byPath[p.ImportPath] = p
		if p.Module != nil && filepath.Clean(p.Module.Dir) == filepath.Clean(root) {
			module = p.Module.Path
		}
	}
	if module == "" {
		return nil, errors.New("go list names no package of the module at the root")
	}
	stops := map[string]bool{}
	for _, dir := range emissionRoots {
		stops[module+"/"+dir] = true
	}
	var queue []string
	for _, root := range driverRoots {
		queue = append(queue, module+"/"+strings.TrimPrefix(root, "./"))
	}
	seen := map[string]bool{}
	var reached []listedPackage
	for len(queue) > 0 {
		path := queue[0]
		queue = queue[1:]
		if seen[path] || stops[path] {
			continue
		}
		seen[path] = true
		p, ok := byPath[path]
		if !ok {
			return nil, fmt.Errorf("go list did not list %s", path)
		}
		if p.Standard || p.Module == nil || p.Module.Path != module {
			continue
		}
		reached = append(reached, p)
		queue = append(queue, p.Imports...)
	}
	slices.SortFunc(reached, func(a, b listedPackage) int { return strings.Compare(a.ImportPath, b.ImportPath) })
	return reached, nil
}

// driverDigest is the SHA-256 of the driver's sources: every file each
// driver package compiles or embeds, by path and bytes, and the module's
// go.mod and go.sum. It fails where root has no driver to list.
func driverDigest(root string) (string, error) {
	packages, err := driverPackages(root)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, name := range []string{"go.mod", "go.sum"} {
		sum, err := fileDigest(filepath.Join(root, name))
		if errors.Is(err, os.ErrNotExist) {
			sum = "absent"
		} else if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "module file %s %s\n", name, sum)
	}
	for _, p := range packages {
		fmt.Fprintf(h, "package %s\n", p.ImportPath)
		for _, group := range [][]string{p.GoFiles, p.CgoFiles, p.CFiles, p.HFiles, p.SFiles, p.SysoFiles, p.EmbedFiles} {
			for _, file := range group {
				sum, err := fileDigest(filepath.Join(p.Dir, file))
				if err != nil {
					return "", err
				}
				fmt.Fprintf(h, "file %s %s\n", file, sum)
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
