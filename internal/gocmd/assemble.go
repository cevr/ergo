package gocmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// File is one file of a lowering, by a slash-separated relative path.
type File struct {
	Path string
	Data []byte
}

// DeclFile is the name the declaration file of an Ergo package (NAME.go
// of the lowering: the Go declarations, IG11 facades and Go bodies) has in
// the package's folder, through the overlay.
const DeclFile = "ergo_gen.go"

// IRFile is the lowering's IR file name (lower.IRFileName).
const IRFile = "ergoir.json"

// GenDir is the module folder of the Go packages the lowering adds, the
// shared and std packages (lower.goPackagesDir). Each is a module of its
// own at its cached tree, required and replaced in the go.mod copy, so
// nothing of it is in the module's folder.
const GenDir = "_ergo"

// runtimeModule is the module path of Ergo's runtime (lower.Runtime).
const runtimeModule = "ergo.local/prototype"

// Plan is the go command line of a build verb with the Ergo packages
// added, and the folder of its go.mod copy, which the caller removes once
// the command ends.
type Plan struct {
	Args   []string
	ModDir string
}

// Assemble writes files, the lowering of the module's Ergo packages pkgs
// (lower.LowerGoPackages: paths relative to the module root), one tree for
// each Go package, and the runtime module (runtime, the files of
// lower.Runtime under ergo/) into cache, content-addressed, and returns
// the command line of l that builds the module m with them.
//
//   - -overlay maps each Ergo package's DeclFile and each asset to its
//     cached copy, merged with the user's overlay.
//   - -modfile is a copy of go.mod (or the user's -modfile) with the runtime
//     module, and each package below GenDir as a module of its own (its
//     tree with a go.mod of go.mod's go version), required and replaced by
//     its cached folder, and go.sum beside it, in a fresh folder under
//     cache.
//   - -gcflags gives each Go package of the lowering -ergoir with its
//     cached IR file after the user's flags for that package
//     (PackageFlags), as the last value that matches it, so cmd/go gives
//     the package exactly those.
//
// The IR file's folder is named by its package's files' digest, and the
// Go file holds the IR's digest (lower.irKeyed), so an edit of a body
// changes what cmd/go keys that package's compile on, and no other's.
func Assemble(cache string, m Module, l Line, cwd string, runtime []File, pkgs []ErgoPackage, files []File) (Plan, error) {
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
	gen := filepath.Join(m.Root, GenDir)
	if _, err := os.Lstat(gen); err == nil {
		return Plan{}, fmt.Errorf("%s exists; ergo writes the Go packages it adds there: remove or rename it", gen)
	}
	ergoAt := map[string]ErgoPackage{} // by module-relative folder, "." for the root
	for _, p := range pkgs {
		rel, err := filepath.Rel(m.Root, p.Dir)
		if err != nil {
			return Plan{}, err
		}
		ergoAt[path.Clean(filepath.ToSlash(rel))] = p
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
	// A package below GenDir is a module of its own, at its cached
	// tree: cmd/go runs vet (and its facts pass over every dependency) in
	// the package's folder, which an overlay cannot supply.
	goLine := directive(mod, "go")
	for _, f := range rt {
		if goLine == "" && f.Path == "go.mod" {
			goLine = directive(f.Data, "go")
		}
	}
	var genMods []string // require and replace pairs: path, folder
	byDir := map[string][]File{}
	for _, f := range files {
		if f.Path == "go.mod" || f.Path == "go.sum" {
			continue
		}
		d := packageDir(f.Path)
		rel := f.Path
		if d != "." {
			rel = strings.TrimPrefix(f.Path, d+"/")
		}
		byDir[d] = append(byDir[d], File{Path: rel, Data: f.Data})
	}
	for _, d := range slices.Sorted(maps.Keys(byDir)) {
		p, isErgo := ergoAt[d]
		if !isErgo && d != GenDir && !strings.HasPrefix(d, GenDir+"/") {
			return Plan{}, fmt.Errorf("gocmd: the lowering wrote the Go package %s, neither an Ergo package of the module nor below %s", d, GenDir)
		}
		tree := byDir[d]
		pkg := p.Package
		if !isErgo {
			pkg.ImportPath = path.Join(m.Path, d)
			gomod := "module " + pkg.ImportPath + "\n"
			if goLine != "" {
				gomod += "\ngo " + goLine + "\n"
			}
			tree = append(slices.Clone(tree), File{Path: "go.mod", Data: []byte(gomod)})
		}
		dir, err := writeTree(cache, "pkg", tree)
		if err != nil {
			return Plan{}, err
		}
		if !isErgo {
			pkg.Dir = dir
			genMods = append(genMods, pkg.ImportPath, dir)
		}
		ir := ""
		for _, f := range byDir[d] {
			if f.Path == IRFile {
				ir = filepath.Join(dir, IRFile)
				continue
			}
			if !isErgo {
				continue // read in its module's folder
			}
			target := filepath.Join(pkg.Dir, filepath.FromSlash(f.Path))
			if isErgo && f.Path == p.Name+".go" {
				target = filepath.Join(pkg.Dir, DeclFile)
			}
			if _, given := replace[target]; given {
				return Plan{}, fmt.Errorf("-overlay names %s, which ergo writes for the Go package %s", target, pkg.ImportPath)
			}
			if _, err := os.Lstat(target); err == nil {
				return Plan{}, fmt.Errorf("%s exists; ergo writes it for the Ergo package %s: remove or rename it", target, pkg.ImportPath)
			}
			replace[target] = filepath.Join(dir, filepath.FromSlash(f.Path))
		}
		user, err := l.PackageFlags(pkg, cwd)
		if err != nil {
			return Plan{}, err
		}
		args := slices.Clone(user)
		if ir != "" {
			args = append(args, "-ergoir="+ir)
		}
		joined, err := JoinQuoted(args)
		if err != nil {
			return Plan{}, fmt.Errorf("-gcflags for %s: %v", pkg.ImportPath, err)
		}
		gcflags = append(gcflags, "-gcflags="+pkg.ImportPath+"="+joined)
	}
	overlay, err := json.Marshal(struct{ Replace map[string]string }{replace})
	if err != nil {
		return Plan{}, err
	}
	ovDir, err := writeTree(cache, "overlay", []File{{Path: "overlay.json", Data: overlay}})
	if err != nil {
		return Plan{}, err
	}
	modDir, err := os.MkdirTemp(cache, "mod-")
	if err != nil {
		return Plan{}, err
	}
	mod = append(slices.Clone(mod), []byte(fmt.Sprintf("\n// Added by ergo go: Ergo's runtime.\nrequire %s v0.0.0\n\nreplace %s => %s\n", runtimeModule, runtimeModule, rtDir))...)
	if len(genMods) > 0 {
		mod = append(mod, "\n// Added by ergo go: the Go packages the lowering adds.\n"...)
		for i := 0; i < len(genMods); i += 2 {
			mod = append(mod, fmt.Sprintf("require %s v0.0.0\nreplace %s => %s\n", genMods[i], genMods[i], genMods[i+1])...)
		}
	}
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

// packageDir is the module-relative folder ("." for the root) of the Go
// package a file of the lowering belongs to: its own folder, or for an
// asset (assets/DIGEST/… beside the Go file that embeds it; lower's
// packageEmbed) the folder that holds assets/.
func packageDir(p string) string {
	parts := strings.Split(p, "/")
	for i := len(parts) - 3; i >= 0; i-- {
		if parts[i] == "assets" && isAssetDigest(parts[i+1]) {
			return path.Clean(path.Join(append([]string{"."}, parts[:i]...)...))
		}
	}
	return path.Dir(p)
}

// isAssetDigest reports the folder name an asset snapshot's digest gives
// its files (check's embed: assets/%x of 12 bytes).
func isAssetDigest(s string) bool {
	if len(s) != 24 {
		return false
	}
	for _, c := range s {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
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
