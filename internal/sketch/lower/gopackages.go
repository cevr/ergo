package lower

import (
	"bytes"
	"fmt"
	"go/token"
	"path"
	"slices"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
)

// Go imports Ergo packages that import each other (ERGO-GO-IMPORTS;
// docs/spec/interop.md, Go calls Ergo). LowerGoPackages lowers every Ergo
// package of a Go module in one lowering and splits it as the IR path's
// package split does (pkgsplit.go), with each Ergo package placed at its
// own folder of the Go module, under its own import path: a Go program that
// reaches a's types through b and through a reaches one Go type, so
// errors.As(err, &a.DivZero{}) holds of a failure a function of b passed
// on, and a value of a's type that b returns is assignable to one a
// returns. A copy of a's declarations in b's Go package, as LowerIRLibrary
// lowers an import, would be a second type of the same name.
//
// The packages the lowering adds, the shared package and one per std
// package the module reaches, are placed below goPackagesDir in the
// module, a folder `./...` does not walk: Go reads them through ergo go's
// overlay only.
//
// Each package's Go-facing names keep their Ergo names in its Go package:
// the facade of each exported function (exports) and each exported type.
// The one Go file the lowering writes names them uniquely, so two
// packages' Add are Add and Add_1 there; the split spells each in its own
// package as its Ergo name (splitLib), and a declaration of its package
// placed elsewhere (an exported type a generated declaration of the shared
// package uses) is named in its package by an alias.
//
// Prior art: Gleam and ReScript write one host module per source module,
// at a path the source module's path decides, and qualify every
// cross-module reference in the host's syntax: a type has one host
// declaration, which every importer names. Kotlin/Native exports a whole
// module as one framework for the same reason: a type exported twice is
// two types to the host.

// goPackagesDir is the folder of the Go module the packages LowerGoPackages
// adds are placed in: an element starting with _ is one `./...` skips,
// and a Go import path may name.
const goPackagesDir = "_ergo"

// goPackagesRoot is the Go package name of the split's root, which a
// lowering of Go packages has no use for: it holds no declaration Go
// imports, and LowerGoPackages writes none of it.
const goPackagesRoot = "ergoroot"

// goPackageDir is an Ergo package's folder in the Go module, relative to
// the module root ("" for the root), and its Go package name.
type goPackageDir struct {
	dir, name string
}

// splitLib is the split of LowerGoPackages: each Ergo package's folder and
// name, and the Go-facing names of the lowering's one Go file.
type splitLib struct {
	pkgs map[*check.Package]goPackageDir
	// names are, by the one Go file's name, the Ergo package of each
	// facade and exported type, and the name Go spells it with there.
	names map[string]libName
	// index is each Ergo package's package in the plan.
	index map[*check.Package]int
}

// libName is a Go-facing declaration's Ergo package and its Ergo name.
type libName struct {
	pkg  *check.Package
	name string
}

// LowerGoPackages lowers each Ergo package of u, the root and every
// import, as the Go package of the Go module module that its import path
// names: a package's files sit in its folder relative to the module root,
// the Go file NAME.go (NAME from names, by Ergo import path, which is
// the Go import path) and its IR file. The shared and std packages sit
// below goPackagesDir. A package's assets sit in the folder of each Go
// package whose Go embeds them. Every package path must be module or
// below it.
func LowerGoPackages(u Unit, module string, names map[string]string) ([]File, IRReport, error) {
	if err := u.Validate(); err != nil {
		return nil, IRReport{}, err
	}
	lib := &splitLib{pkgs: map[*check.Package]goPackageDir{}}
	for _, un := range append([]Unit{u}, u.Imports...) {
		p := un.Package
		rel, ok := strings.CutPrefix(p.Path, module)
		if !ok || rel != "" && rel[0] != '/' {
			return nil, IRReport{}, fmt.Errorf("lower: the Ergo package %s is not a package of the Go module %s", p.Path, module)
		}
		rel = strings.TrimPrefix(rel, "/")
		if rel == goPackagesDir || strings.HasPrefix(rel, goPackagesDir+"/") {
			return nil, IRReport{}, fmt.Errorf("lower: the Ergo package %s is in %s, the folder of the packages ergo adds", p.Path, goPackagesDir)
		}
		name := names[p.Path]
		if !token.IsIdentifier(name) || name == "main" {
			return nil, IRReport{}, fmt.Errorf("lower: %q is not a Go package name for the Ergo package %s", name, p.Path)
		}
		lib.pkgs[p] = goPackageDir{dir: rel, name: name}
	}
	u.goPackages = true
	files, origins, report, l, err := lowerIRModuleReaching(u, nil, false, splitFormatLowering != "format", nil)
	if err != nil {
		return nil, IRReport{}, err
	}
	i := slices.IndexFunc(files, func(f File) bool { return isMainGo(f.Path) })
	if i < 0 {
		return nil, IRReport{}, fmt.Errorf("lower: the lowering of the Go packages wrote no Go file")
	}
	lib.names = l.libNames()
	out, rep := splitMain(u, files[i].Data, module, origins, nil, l.sitePkgNames(), &l.ir.out, nil, lib)
	if rep.err != nil {
		return nil, IRReport{}, rep.err
	}
	if out == nil {
		return nil, IRReport{}, fmt.Errorf("lower: the Go packages do not split: %s", rep.Reason)
	}
	// An asset sits beside each Go file that embeds it: a //go:embed
	// pattern names a file of its own package's folder.
	var assets []File
	for _, f := range files {
		if !strings.HasPrefix(f.Path, "assets/") {
			continue
		}
		for _, g := range out {
			if strings.HasSuffix(g.Path, ".go") && bytes.Contains(g.Data, []byte(`"`+f.Path+`"`)) {
				assets = append(assets, File{Path: path.Join(path.Dir(g.Path), f.Path), Data: f.Data})
			}
		}
	}
	res := irKeyed(append(out, assets...))
	report.Hash = irHash(res)
	return res, report.IRReport, nil
}

// libNames are the Go-facing names of a lowering of Go packages
// (splitLib.names): each export's facade and each exported type of a
// sourced package, by the name the one Go file gives it.
func (l *lowerer) libNames() map[string]libName {
	sourced := map[*check.Package]bool{}
	for _, p := range l.sourcedPkgs() {
		sourced[p] = true
	}
	out := map[string]libName{}
	for key, name := range l.pkgNames {
		switch o := key.obj.(type) {
		case *check.Func:
			if key.kind == "export" {
				out[name] = libName{pkg: o.Pkg, name: o.Name}
			}
		case *check.TypeName:
			if key.kind == "type" && sourced[o.Pkg] && exported(o.Name) {
				out[name] = libName{pkg: o.Pkg, name: o.Name}
			}
		}
	}
	return out
}
