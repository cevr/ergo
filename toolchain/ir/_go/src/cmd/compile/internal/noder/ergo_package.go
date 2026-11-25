package noder

import (
	"flag"
	"fmt"
	"internal/buildcfg"
	"internal/pkgbits"
	"os"
	"strings"

	"cmd/compile/internal/base"
	"cmd/compile/internal/ergoir"
	"cmd/compile/internal/inline"
	"cmd/compile/internal/ir"
	"cmd/compile/internal/pgoir"
	"cmd/compile/internal/typecheck"
	"cmd/compile/internal/types"
	"cmd/internal/obj"
)

// ergoLoadPackage is the single guarded ingress for -ergopkg.
// When -ergopkg is off, it returns false immediately so stock Go compilation
// runs without any behavioral or output difference.
func ergoLoadPackage(filenames []string) bool {
	if !ergoir.PkgFlag {
		return false
	}

	if err := loadErgoPackage(filenames); err != nil {
		base.Errorf("%v", err)
		base.ErrorExit()
	}
	return true
}

func loadErgoPackage(filenames []string) error {
	if ergoir.Flag.IR != "" {
		return fmt.Errorf("ergopkg: -ergopkg cannot be combined with -ergoir")
	}
	if len(filenames) != 1 {
		return fmt.Errorf("ergopkg: expected exactly one checked EIR package file, got %d", len(filenames))
	}
	if strings.HasSuffix(filenames[0], ".go") {
		return fmt.Errorf("ergopkg: -ergopkg cannot be combined with .go source file %q", filenames[0])
	}

	var sawP, sawO, sawCov bool
	flag.CommandLine.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "p":
			sawP = true
		case "o":
			sawO = true
		case "coveragecfg":
			sawCov = true
		}
	})
	if !sawP || base.Ctxt.Pkgpath == "" || base.Ctxt.Pkgpath == obj.UnlinkablePkg {
		return fmt.Errorf("ergopkg: explicit -p package path is required")
	}
	if !sawO || base.Flag.LowerO == "" {
		return fmt.Errorf("ergopkg: explicit -o output path is required")
	}

	wire, err := os.ReadFile(filenames[0])
	if err != nil {
		return fmt.Errorf("ergopkg: reading %q: %w", filenames[0], err)
	}
	eir, err := ergoir.DecodeEIR(wire)
	if err != nil {
		return err
	}

	profile := ergoActiveProfile(sawCov)
	pin := buildcfg.Version
	if fields := strings.Fields(pin); len(fields) > 0 {
		pin = fields[0]
	}
	wantTarget := ergoir.EIRTarget{
		Path:    base.Ctxt.Pkgpath,
		GOOS:    buildcfg.GOOS,
		GOARCH:  buildcfg.GOARCH,
		Pin:     pin,
		Profile: profile,
	}

	type openedImport struct {
		resolved string
		data     string
		pr       pkgbits.PkgDecoder
	}
	opened := make([]openedImport, len(eir.Imports))
	observed := make([]ergoir.EIRImport, len(eir.Imports))

	for i, imp := range eir.Imports {
		resolved, err := resolveImportPath(imp.Path)
		if err != nil {
			return fmt.Errorf("ergopkg: import %q: %w", imp.Path, err)
		}
		if resolved == "unsafe" {
			return fmt.Errorf("ergopkg: import %q is not a Go archive import", imp.Path)
		}
		f, err := openPackage(resolved)
		if err != nil {
			return fmt.Errorf("ergopkg: opening import %q: %w", imp.Path, err)
		}
		data, err := readExportData(f)
		f.Close()
		if err != nil {
			return fmt.Errorf("ergopkg: reading export data of %q: %w", imp.Path, err)
		}
		pr := pkgbits.NewPkgDecoder(resolved, data)
		obsName, obsHasInit := inspectImportedArchive(&pr)
		opened[i] = openedImport{resolved: resolved, data: data, pr: pr}
		observed[i] = ergoir.EIRImport{
			Path:        imp.Path,
			Name:        obsName,
			HasInit:     obsHasInit,
			Fingerprint: pr.Fingerprint(),
		}
	}

	if err := ergoir.AdmitEIR(eir, wantTarget, observed); err != nil {
		return err
	}

	// Build unified stub before mutating compiler state so unsupported EIR
	// constructs fail closed before producing any output.
	stubData, err := writeErgoPkgStub(eir)
	if err != nil {
		return err
	}

	target := typecheck.Target
	types.LocalPkg.Name = eir.PkgName

	for _, imp := range opened {
		pkg1 := types.NewPkg(imp.resolved, "")
		if !pkg1.Direct {
			pkg1.Direct = true
			target.Imports = append(target.Imports, pkg1)
			readPackage(newPkgReader(imp.pr), pkg1, false)
			if err := addFingerprint(imp.resolved, imp.data); err != nil {
				return fmt.Errorf("ergopkg: fingerprint for %q: %w", imp.resolved, err)
			}
		}
	}

	inline.InlineCall = unifiedInlineCall
	typecheck.HaveInlineBody = unifiedHaveInlineBody
	pgoir.LookupFunc = LookupFunc
	pgoir.PostLookupCleanup = PostLookupCleanup

	localPkgReader = newPkgReader(pkgbits.NewPkgDecoder(types.LocalPkg.Path, stubData))
	readPackage(localPkgReader, types.LocalPkg, true)

	r := localPkgReader.newReader(pkgbits.SectionMeta, pkgbits.PrivateRootIdx, pkgbits.SyncPrivate)
	r.pkgInit(types.LocalPkg, target)

	readBodies(target, false, nil)

	for _, fn := range target.Funcs {
		if fn.Typecheck() == 0 {
			base.FatalfAt(fn.Pos(), "missed typecheck: %v", fn)
		}
		if len(fn.Body) != 0 {
			if stmt := fn.Body[0]; stmt.Typecheck() == 0 {
				base.FatalfAt(stmt.Pos(), "missed typecheck: %v", stmt)
			}
		}
	}
	for _, fn := range target.Funcs {
		if !base.Flag.CompilingRuntime && types.RuntimeSymName(fn.Sym()) != "" {
			fn.Pragma |= ir.Norace
		}
	}

	base.ExitIfErrors()
	return nil
}

func ergoActiveProfile(sawCov bool) string {
	var flags []string
	if base.Flag.Race {
		flags = append(flags, "race")
	}
	if base.Flag.MSan {
		flags = append(flags, "msan")
	}
	if base.Flag.ASan {
		flags = append(flags, "asan")
	}
	if base.Flag.Dynlink != nil && *base.Flag.Dynlink {
		flags = append(flags, "dynlink")
	}
	if base.Flag.Shared != nil && *base.Flag.Shared {
		flags = append(flags, "shared")
	}
	if sawCov {
		flags = append(flags, "cover")
	}
	if len(flags) == 0 {
		return "plain"
	}
	return strings.Join(flags, "+")
}

func inspectImportedArchive(pr *pkgbits.PkgDecoder) (pkgName string, hasInit bool) {
	pub := pr.NewDecoder(pkgbits.SectionMeta, pkgbits.PublicRootIdx, pkgbits.SyncPublic)
	pub.Sync(pkgbits.SyncPkg)
	pkgIdx := pub.Reloc(pkgbits.SectionPkg)

	pdec := pr.NewDecoder(pkgbits.SectionPkg, pkgIdx, pkgbits.SyncPkgDef)
	_ = pdec.String() // path
	pkgName = pdec.String()

	priv := pr.NewDecoder(pkgbits.SectionMeta, pkgbits.PrivateRootIdx, pkgbits.SyncPrivate)
	hasInit = priv.Bool()
	return pkgName, hasInit
}
