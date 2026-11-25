package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"

	"ergo.local/prototype/internal/bindc"
	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/sketchfront"
)

// bindCCommand is `ergo bind c [-o DIR] HEADER CONTRACT` (CFFI-2).
//
// It parses HEADER against CONTRACT, generates the CGO_ENABLED=0 purego Go
// package via bindc.GoPackage and the checked Ergo binding via bindc.ErgoText,
// and writes both:
//
//   - With -o DIR:
//     Go package -> DIR/<package>/<package>.go
//     Ergo binding -> DIR/ergo.bind/<import_path>/<base>.ergo
//   - Inside an Ergo module (ergo.mod + go.mod above cwd):
//     Go package -> <module root>/<rel>/<package>.go (when <import_path> is
//     under go.mod's module path) or <module root>/<package>/<package>.go
//     Ergo binding (stamped with `// ergo:module <mod>`) ->
//     <module root>/ergo.bind/<import_path>/<base>.ergo
//   - Outside a module without -o:
//     writes to current directory (`./<package>/<package>.go` and
//     `./ergo.bind/<import_path>/<base>.ergo`).
func bindCCommand(args []string) error {
	fs := flag.NewFlagSet("bind c", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	out := fs.String("o", "", "")
	const usage = "usage: ergo bind c [-o DIR] HEADER CONTRACT"
	if err := fs.Parse(args); err != nil {
		return flagError(err, usage)
	}
	if fs.NArg() != 2 {
		return usageError{message: usage}
	}
	headerPath, contractPath := fs.Arg(0), fs.Arg(1)
	headerSrc, err := os.ReadFile(headerPath)
	if err != nil {
		return exitStatusError{code: 1, message: "ergo bind c: " + err.Error()}
	}
	contractSrc, err := os.ReadFile(contractPath)
	if err != nil {
		return exitStatusError{code: 1, message: "ergo bind c: " + err.Error()}
	}
	model, err := bindc.Parse(headerPath, headerSrc)
	if err != nil {
		return exitStatusError{code: 1, message: "ergo bind c: " + err.Error()}
	}
	binding, err := bindc.Bind(model, headerSrc, contractSrc)
	if err != nil {
		return exitStatusError{code: 1, message: "ergo bind c: " + err.Error()}
	}
	goFile, goSrc, err := bindc.GoPackage(binding)
	if err != nil {
		return exitStatusError{code: 1, message: "ergo bind c: " + err.Error()}
	}
	ergoBytes, err := bindc.ErgoText(binding)
	if err != nil {
		return exitStatusError{code: 1, message: "ergo bind c: " + err.Error()}
	}
	ergoSrc := string(ergoBytes)

	importPath := binding.Contract.Go.ImportPath
	pkgName := binding.Contract.Go.Package
	bindRel := filepath.FromSlash(pathpkg.Join(sketchfront.BindFolder, importPath, pathpkg.Base(importPath)+".ergo"))

	var goOutPath, ergoOutPath string
	if *out != "" {
		goOutPath = filepath.Join(*out, pkgName, goFile)
		ergoOutPath = filepath.Join(*out, bindRel)
	} else {
		dir, module, err := bindRoot()
		if err != nil {
			return exitStatusError{code: 1, message: "ergo bind c: " + err.Error()}
		}
		if module {
			modPath, err := readGoModModulePath(filepath.Join(dir, sketchfront.GoModFile))
			if err != nil {
				return exitStatusError{code: 1, message: "ergo bind c: " + err.Error()}
			}
			if modPath != "" {
				ergoSrc = insertCBindModuleStamp(ergoSrc, modPath)
			}
			relPkg := pkgName
			if modPath != "" && strings.HasPrefix(importPath, modPath+"/") {
				relPkg = filepath.FromSlash(strings.TrimPrefix(importPath, modPath+"/"))
			}
			goOutPath = filepath.Join(dir, relPkg, goFile)
			ergoOutPath = filepath.Join(dir, bindRel)
		} else {
			goOutPath = filepath.Join(dir, pkgName, goFile)
			ergoOutPath = filepath.Join(dir, bindRel)
		}
	}

	for _, w := range []struct {
		path string
		data []byte
	}{
		{goOutPath, goSrc},
		{ergoOutPath, []byte(ergoSrc)},
	} {
		if err := os.MkdirAll(filepath.Dir(w.path), 0o755); err != nil {
			return exitStatusError{code: 1, message: "ergo bind c: " + err.Error()}
		}
		if err := os.WriteFile(w.path, w.data, 0o644); err != nil {
			return exitStatusError{code: 1, message: "ergo bind c: " + err.Error()}
		}
	}
	return nil
}

// readGoModModulePath extracts the `module <path>` directive from a go.mod file.
func readGoModModulePath(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "module "); ok {
			fields := strings.Fields(rest)
			if len(fields) > 0 {
				return strings.Trim(fields[0], `"`), nil
			}
		}
	}
	return "", fmt.Errorf("%s has no module directive", path)
}

// insertCBindModuleStamp inserts `// ergo:module <modPath>` right after the
// `// ergo:bind c ...` header line so sketchfront's staleBinding check
// validates that go.mod owns the generated Go package.
func insertCBindModuleStamp(src, modPath string) string {
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "// ergo:bind c ") {
			stamp := gobind.ModuleLine + modPath
			out := make([]string, 0, len(lines)+1)
			out = append(out, lines[:i+1]...)
			out = append(out, stamp)
			out = append(out, lines[i+1:]...)
			return strings.Join(out, "\n")
		}
	}
	return gobind.ModuleLine + modPath + "\n" + src
}
