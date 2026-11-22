package gocmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// File is one file of a lowering, by a slash-separated relative path.
type File struct {
	Path string
	Data []byte
}

// Lowered is an Ergo package lowered for Go: its declaration file
// (NAME.go, the Go declarations, IG11 facades and Go bodies), its IR file
// (ergoir.json) and the assets its Go embeds.
type Lowered struct {
	Package ErgoPackage
	Files   []File
}

// DeclFile is the name the declaration file has in the package's folder,
// through the overlay.
const DeclFile = "ergo_gen.go"

// IRFile is the lowering's IR file name (lower.IRFileName).
const IRFile = "ergoir.json"

// runtimeModule is the module path of Ergo's runtime (lower.Runtime).
const runtimeModule = "ergo.local/prototype"

// Plan is the go command line of a build verb with the Ergo packages
// added, and the folder of its go.mod copy, which the caller removes once
// the command ends.
type Plan struct {
	Args   []string
	ModDir string
}

// Assemble writes pkgs and the runtime module (runtime, the files of
// lower.Runtime under ergo/) into cache, content-addressed, and returns
// the command line of l that builds the module m with them.
//
//   - -overlay maps each package's DeclFile, and each asset, to its cached
//     copy, merged with the user's overlay.
//   - -modfile is a copy of go.mod (or the user's -modfile) with the runtime
//     module required and replaced by its cached folder, and go.sum beside
//     it, in a fresh folder under cache.
//   - -gcflags gives each Ergo package -ergoir with its cached IR file after
//     the user's flags for that package (PackageFlags), as the last value
//     that matches it, so cmd/go gives the package exactly those.
//
// The IR file's folder is named by the lowering's digest, and the
// declaration file holds the IR's digest (lower.irKeyed), so an edit of
// a body changes what cmd/go keys the compile on.
func Assemble(cache string, m Module, l Line, cwd string, runtime []File, pkgs []Lowered) (Plan, error) {
	var rt []File
	for _, f := range runtime {
		rel, ok := strings.CutPrefix(f.Path, "ergo/")
		if !ok {
			return Plan{}, fmt.Errorf("gocmd: the runtime file %s is not under ergo/", f.Path)
		}
		rt = append(rt, File{Path: rel, Data: f.Data})
	}
	rtDir, err := writeTree(cache, "runtime", rt)
	if err != nil {
		return Plan{}, err
	}
	replace := map[string]string{}
	if l.Overlay != "" {
		data, err := os.ReadFile(l.Overlay)
		if err != nil {
			return Plan{}, fmt.Errorf("-overlay: %v", err)
		}
		var user struct{ Replace map[string]string }
		if err := json.Unmarshal(data, &user); err != nil {
			return Plan{}, fmt.Errorf("-overlay %s: %v", l.Overlay, err)
		}
		for k, v := range user.Replace {
			if !filepath.IsAbs(k) {
				k = filepath.Join(cwd, k)
			}
			if v != "" && !filepath.IsAbs(v) {
				v = filepath.Join(cwd, v)
			}
			replace[k] = v
		}
	}
	var gcflags []string
	for _, v := range l.GCFlags {
		gcflags = append(gcflags, "-gcflags="+v)
	}
	for _, p := range pkgs {
		dir, err := writeTree(cache, "pkg", p.Files)
		if err != nil {
			return Plan{}, err
		}
		ir := ""
		for _, f := range p.Files {
			switch {
			case f.Path == IRFile:
				ir = filepath.Join(dir, IRFile)
				continue
			case f.Path == "go.mod" || f.Path == "go.sum":
				continue
			}
			target := filepath.Join(p.Package.Dir, filepath.FromSlash(f.Path))
			if f.Path == p.Package.Name+".go" {
				target = filepath.Join(p.Package.Dir, DeclFile)
			}
			if _, given := replace[target]; given {
				return Plan{}, fmt.Errorf("-overlay names %s, which ergo writes for the Ergo package %s", target, p.Package.ImportPath)
			}
			if _, err := os.Lstat(target); err == nil {
				return Plan{}, fmt.Errorf("%s exists; ergo writes it for the Ergo package %s: remove or rename it", target, p.Package.ImportPath)
			}
			replace[target] = filepath.Join(dir, filepath.FromSlash(f.Path))
		}
		user, err := l.PackageFlags(p.Package.Package, cwd)
		if err != nil {
			return Plan{}, err
		}
		args := slices.Clone(user)
		if ir != "" {
			args = append(args, "-ergoir="+ir)
		}
		joined, err := JoinQuoted(args)
		if err != nil {
			return Plan{}, fmt.Errorf("-gcflags for %s: %v", p.Package.ImportPath, err)
		}
		gcflags = append(gcflags, "-gcflags="+p.Package.ImportPath+"="+joined)
	}
	overlay, err := json.Marshal(struct{ Replace map[string]string }{replace})
	if err != nil {
		return Plan{}, err
	}
	ovDir, err := writeTree(cache, "overlay", []File{{Path: "overlay.json", Data: overlay}})
	if err != nil {
		return Plan{}, err
	}
	mod, sum := m.GoMod, filepath.Join(m.Root, "go.sum")
	if l.Modfile != "" {
		path := l.Modfile
		if !filepath.IsAbs(path) {
			path = filepath.Join(cwd, path)
		}
		if mod, err = os.ReadFile(path); err != nil {
			return Plan{}, fmt.Errorf("-modfile: %v", err)
		}
		sum = strings.TrimSuffix(path, ".mod") + ".sum"
	}
	if requires(mod, runtimeModule) {
		return Plan{}, fmt.Errorf("go.mod names %s, Ergo's runtime, which ergo go supplies itself: remove its require and replace lines", runtimeModule)
	}
	modDir, err := os.MkdirTemp(cache, "mod-")
	if err != nil {
		return Plan{}, err
	}
	mod = append(slices.Clone(mod), []byte(fmt.Sprintf("\n// Added by ergo go: Ergo's runtime.\nrequire %s v0.0.0\n\nreplace %s => %s\n", runtimeModule, runtimeModule, rtDir))...)
	err = os.WriteFile(filepath.Join(modDir, "go.mod"), mod, 0o644)
	if err == nil {
		var data []byte
		data, err = os.ReadFile(sum)
		if errors.Is(err, fs.ErrNotExist) {
			data, err = nil, nil
		}
		if err == nil {
			err = os.WriteFile(filepath.Join(modDir, "go.sum"), data, 0o644)
		}
	}
	if err != nil {
		os.RemoveAll(modDir)
		return Plan{}, err
	}
	args := []string{l.Verb, "-modfile=" + filepath.Join(modDir, "go.mod"), "-overlay=" + filepath.Join(ovDir, "overlay.json")}
	args = append(args, gcflags...)
	return Plan{Args: append(args, l.Args...), ModDir: modDir}, nil
}

// requires reports whether go.mod text names the module path in a
// require or replace directive.
func requires(mod []byte, path string) bool {
	for _, line := range strings.Split(string(mod), "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		for _, f := range strings.Fields(line) {
			if strings.Trim(f, `"`) == path {
				return true
			}
		}
	}
	return false
}

// writeTree writes files into cache/kind-DIGEST, named by their paths and
// bytes, once: an existing folder is the same files. It is written
// under a temporary name and renamed, so a concurrent writer of the same
// tree finds it whole.
func writeTree(cache, kind string, files []File) (string, error) {
	files = slices.Clone(files)
	slices.SortFunc(files, func(a, b File) int { return strings.Compare(a.Path, b.Path) })
	h := sha256.New()
	for _, f := range files {
		fmt.Fprintf(h, "%q %d\n", f.Path, len(f.Data))
		h.Write(f.Data)
	}
	dir := filepath.Join(cache, kind+"-"+hex.EncodeToString(h.Sum(nil))[:32])
	if _, err := os.Stat(dir); err == nil {
		return dir, nil
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(cache, ".tmp-"+kind+"-")
	if err != nil {
		return "", err
	}
	for _, f := range files {
		p := filepath.Join(tmp, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			os.RemoveAll(tmp)
			return "", err
		}
		if err := os.WriteFile(p, f.Data, 0o644); err != nil {
			os.RemoveAll(tmp)
			return "", err
		}
	}
	if err := os.Rename(tmp, dir); err != nil {
		os.RemoveAll(tmp)
		if _, statErr := os.Stat(dir); statErr == nil {
			return dir, nil
		}
		return "", err
	}
	return dir, nil
}
