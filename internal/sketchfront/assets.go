package sketchfront

import (
	"fmt"
	"os"
	pathpkg "path"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/contentmap"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/lawcache"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/stage"
)

type mapRule struct {
	pattern    string
	mapperPkg  string
	mapperFunc string
	companions []string
	line       int
	col        int
}

type assetMeta struct {
	rule       mapRule
	relPath    string
	absPath    string
	bytes      []byte
	companions []eval.MapperCompanion
	importFile loadedFile
	importSpec *syntax.ImportSpec
	importPkg  *loadedPackage
}

func defaultMapperCache() (check.MapperCache, string) {
	id, err := build.CompilerIdentity()
	if err != nil || id == "" {
		id = "ergo-dev"
	}
	if os.Getenv("ERGO_LAWCACHE") == "off" {
		return nil, id
	}
	root, err := build.DefaultRoot()
	if err != nil || root == "" {
		return nil, id
	}
	store := lawcache.Open(root, id)
	if store == nil {
		return nil, id
	}
	return store, id
}

func parseMapRule(line, text string, lines []int, lineNum int, fields []string) (mapRule, *report.Diagnostic) {
	if len(fields) < 3 {
		return mapRule{}, modDiagnostic(text, lines, lineNum, 1, "ergo.mod map line needs: map PATTERN PKG.FUNC [COMPANIONS...]")
	}
	pattern := filepath.ToSlash(filepath.Clean(fields[1]))
	pCol := strings.Index(line, fields[1]) + 1
	if reason := assetPatternProblem(fields[1], pattern); reason != "" {
		return mapRule{}, modDiagnostic(text, lines, lineNum, pCol, reason)
	}
	mCol := strings.Index(line, fields[2]) + 1
	mapperPkg, mapperFunc, reason := splitMapperSpec(fields[2])
	if reason != "" {
		return mapRule{}, modDiagnostic(text, lines, lineNum, mCol, reason)
	}
	var companions []string
	for _, rawComp := range fields[3:] {
		cClean := filepath.ToSlash(filepath.Clean(rawComp))
		cCol := strings.LastIndex(line, rawComp) + 1
		if cReason := companionPatternProblem(rawComp, cClean); cReason != "" {
			return mapRule{}, modDiagnostic(text, lines, lineNum, cCol, cReason)
		}
		companions = append(companions, cClean)
	}
	col := strings.Index(line, "map") + 1
	return mapRule{
		pattern:    pattern,
		mapperPkg:  mapperPkg,
		mapperFunc: mapperFunc,
		companions: companions,
		line:       lineNum,
		col:        col,
	}, nil
}

func assetPatternProblem(raw, cleaned string) string {
	if raw == "" || strings.HasPrefix(raw, "/") || strings.Contains(raw, `\`) || strings.HasPrefix(cleaned, "..") || cleaned == "." {
		return fmt.Sprintf("invalid map pattern %q: must be a relative path or glob inside the module", raw)
	}
	base := pathpkg.Base(cleaned)
	if strings.HasSuffix(base, ".ergo") || base == ModFile || base == "ergo.laws" || base == GoModFile {
		return fmt.Sprintf("invalid map pattern %q: content mappers cannot override Ergo source or manifest files", raw)
	}
	if _, err := pathpkg.Match(cleaned, "test"); err != nil {
		return fmt.Sprintf("invalid map pattern %q: %v", raw, err)
	}
	return ""
}

func companionPatternProblem(raw, cleaned string) string {
	if raw == "" || strings.HasPrefix(raw, "/") || strings.Contains(raw, `\`) || strings.HasPrefix(cleaned, "..") || cleaned == "." {
		return fmt.Sprintf("invalid companion pattern %q: must stay inside the module root", raw)
	}
	if _, err := pathpkg.Match(cleaned, "test"); err != nil {
		return fmt.Sprintf("invalid companion pattern %q: %v", raw, err)
	}
	return ""
}

func splitMapperSpec(spec string) (pkgPath, fnName, reason string) {
	lastSlash := strings.LastIndexByte(spec, '/')
	tail := spec[lastSlash+1:]
	dot := strings.LastIndexByte(tail, '.')
	if dot <= 0 || dot == len(tail)-1 {
		return "", "", fmt.Sprintf("invalid mapper %q in ergo.mod: expected PKG.Func", spec)
	}
	splitAt := lastSlash + 1 + dot
	pkgPath = spec[:splitAt]
	fnName = spec[splitAt+1:]
	if pkgPath == "" || !isValidErgoIdent(fnName) {
		return "", "", fmt.Sprintf("invalid mapper %q in ergo.mod: expected PKG.Func with a valid Ergo function name", spec)
	}
	for _, seg := range strings.Split(pkgPath, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.ContainsAny(seg, `\`) {
			return "", "", fmt.Sprintf("invalid mapper package path %q in ergo.mod", pkgPath)
		}
	}
	return pkgPath, fnName, ""
}

func isValidErgoIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if !(r == '_' || unicode.IsLetter(r) || (i > 0 && unicode.IsDigit(r))) {
			return false
		}
	}
	return true
}

func matchAssetPattern(pattern, relSlash string) bool {
	if ok, err := pathpkg.Match(pattern, relSlash); err == nil && ok {
		return true
	}
	if !strings.Contains(pattern, "/") {
		if ok, err := pathpkg.Match(pattern, pathpkg.Base(relSlash)); err == nil && ok {
			return true
		}
	}
	return false
}

func (l *loader) mapsFor(mod *depModule) []mapRule {
	if mod != nil {
		return mod.mod.maps
	}
	return l.maps
}

func (l *loader) modRootOf(mod *depModule) (root, modName string) {
	if mod != nil {
		return mod.dir, mod.name
	}
	return l.root, l.module
}

func (l *loader) matchRule(mod *depModule, relSlash string) (mapRule, bool) {
	for _, r := range l.mapsFor(mod) {
		if matchAssetPattern(r.pattern, relSlash) {
			return r, true
		}
	}
	return mapRule{}, false
}

func (l *loader) resolvedImportPath(p *loadedPackage, rawPath string) string {
	if p != nil && p.resolvedImports != nil {
		if canon, ok := p.resolvedImports[rawPath]; ok {
			return canon
		}
	}
	return rawPath
}

func (l *loader) visitAssetImport(p *loadedPackage, imp localImport, stack []*loadedPackage) (handled bool, err error) {
	isRel := syntax.IsRelativeAssetPath(imp.path)
	if !isRel && len(l.mapsFor(p.mod)) == 0 {
		return false, nil
	}
	modRoot, modName := l.modRootOf(p.mod)
	if modRoot == "" {
		if isRel {
			l.missing[imp.path] = "no " + ModFile + " declares a module or map rules for asset imports"
			return true, nil
		}
		return false, nil
	}

	var absAsset string
	if isRel {
		absAsset = filepath.Clean(filepath.Join(p.dir, filepath.FromSlash(imp.path)))
	} else if lp, ok := l.local(imp.path); ok {
		info, stErr := l.fsys.Stat(lp)
		if stErr != nil || !info.Mode().IsRegular() {
			return false, nil
		}
		absAsset = lp
	} else {
		return false, nil
	}

	rel, relErr := filepath.Rel(modRoot, absAsset)
	relSlash := filepath.ToSlash(rel)
	if relErr != nil || rel == "." || strings.HasPrefix(relSlash, "..") || filepath.IsAbs(rel) {
		l.missing[imp.path] = fmt.Sprintf("asset import %s escapes module root", imp.path)
		return true, nil
	}
	if strings.HasSuffix(relSlash, ".ergo") {
		l.missing[imp.path] = fmt.Sprintf("asset import %s cannot target an .ergo source file directly", imp.path)
		return true, nil
	}

	canonPath := modName + "/" + relSlash
	if p.mod != nil {
		canonPath = p.mod.path + "/" + relSlash
	}
	if p.resolvedImports == nil {
		p.resolvedImports = map[string]string{}
	}
	p.resolvedImports[imp.path] = canonPath

	if existing := l.pkgs[canonPath]; existing != nil {
		switch l.state[existing] {
		case visiting:
			l.cycle(p, imp, existing, stack)
		case unvisited:
			if err := l.visit(existing, stack); err != nil {
				return true, err
			}
		}
		return true, nil
	}

	rule, hasRule := l.matchRule(p.mod, relSlash)
	if !hasRule {
		l.missing[canonPath] = fmt.Sprintf("no ergo.mod map rule matches asset %s", relSlash)
		return true, nil
	}

	data, rErr := l.fsys.ReadFile(absAsset)
	if rErr != nil {
		l.missing[canonPath] = fmt.Sprintf("cannot read asset %s: %v", relSlash, rErr)
		return true, nil
	}
	l.texts[relSlash] = string(data)

	companions, compReason := l.loadCompanions(modRoot, p.mod, rule.companions)
	if compReason != "" {
		l.missing[canonPath] = compReason
		return true, nil
	}

	ap := &loadedPackage{
		path:        canonPath,
		dir:         filepath.Dir(absAsset),
		mod:         p.mod,
		isAsset:     true,
		importOwner: p,
		assetMeta: &assetMeta{
			rule:       rule,
			relPath:    relSlash,
			absPath:    absAsset,
			bytes:      data,
			companions: companions,
			importFile: p.files[imp.file],
			importSpec: imp.spec,
			importPkg:  p,
		},
	}
	ap.imports = []localImport{{path: rule.mapperPkg, file: imp.file, spec: imp.spec}}
	l.pkgs[canonPath] = ap
	if err := l.visit(ap, stack); err != nil {
		return true, err
	}
	return true, nil
}

func (l *loader) loadCompanions(modRoot string, mod *depModule, patterns []string) ([]eval.MapperCompanion, string) {
	if len(patterns) == 0 {
		return nil, ""
	}
	seen := map[string]bool{}
	var out []eval.MapperCompanion
	for _, pat := range patterns {
		matches, err := l.collectCompanionPaths(modRoot, pat)
		if err != nil {
			return nil, fmt.Sprintf("invalid companion pattern %s: %v", pat, err)
		}
		for _, abs := range matches {
			rel, _ := filepath.Rel(modRoot, abs)
			relSlash := filepath.ToSlash(rel)
			if strings.HasPrefix(relSlash, "..") || filepath.IsAbs(rel) {
				return nil, fmt.Sprintf("companion path %s escapes module root", relSlash)
			}
			if seen[relSlash] {
				continue
			}
			seen[relSlash] = true
			bytes, rErr := l.fsys.ReadFile(abs)
			if rErr != nil {
				return nil, fmt.Sprintf("cannot read companion asset %s: %v", relSlash, rErr)
			}
			diagKey := l.relIn(mod, abs)
			l.texts[diagKey] = string(bytes)
			out = append(out, eval.MapperCompanion{Path: diagKey, Bytes: bytes})
		}
	}
	return out, ""
}

func (l *loader) collectCompanionPaths(root, pattern string) ([]string, error) {
	var all []string
	var walk func(dir string) error
	walk = func(dir string) error {
		entries, err := l.fsys.ReadDir(dir)
		if err != nil {
			if dir == root {
				return err
			}
			return nil
		}
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			full := filepath.Join(dir, name)
			if e.IsDir() {
				if err := walk(full); err != nil {
					return err
				}
				continue
			}
			rel, _ := filepath.Rel(root, full)
			relSlash := filepath.ToSlash(rel)
			if ok, err := pathpkg.Match(pattern, relSlash); err == nil && ok {
				all = append(all, full)
			}
		}
		return nil
	}
	if !strings.ContainsAny(pattern, "*?[") {
		abs := filepath.Clean(filepath.Join(root, filepath.FromSlash(pattern)))
		info, err := l.fsys.Stat(abs)
		if err != nil {
			return nil, err
		}
		if info.Mode().IsRegular() {
			return []string{abs}, nil
		}
		return nil, fmt.Errorf("%s is not a regular file", pattern)
	}
	if err := walk(root); err != nil {
		return nil, err
	}
	slices.Sort(all)
	return all, nil
}

func (l *loader) checkAssetPackage(p *loadedPackage) {
	prevChecking := l.checking
	l.checking = p
	defer func() { l.checking = prevChecking }()
	meta := p.assetMeta
	for _, imp := range p.imports {
		q := l.pkgs[l.resolvedImportPath(p, imp.path)]
		if q != nil && q.broken {
			p.broken = true
		}
	}
	if p.broken {
		return
	}
	mapperPkg, impErr := l.Import(meta.rule.mapperPkg)
	if impErr != nil || mapperPkg == nil {
		p.broken = true
		return
	}
	var mapperFn *check.Func
	for _, fn := range mapperPkg.Funcs {
		if fn != nil && fn.Name == meta.rule.mapperFunc && !fn.Ptr && (fn.Decl == nil || fn.Decl.Recv == nil) {
			mapperFn = fn
			break
		}
	}
	impLine, impCol := 1, 1
	var impOff, impLen int
	if meta.importFile.syntax != nil && meta.importSpec != nil {
		impLine, impCol = meta.importFile.syntax.Position(meta.importSpec.Pos())
		impOff = int(meta.importSpec.Pos())
		impLen = int(meta.importSpec.End() - meta.importSpec.Pos())
	}
	if mapperFn == nil {
		p.broken = true
		l.diags = append(l.diags, report.Diagnostic{
			Code:    check.CodeType,
			File:    meta.importFile.rel,
			Message: fmt.Sprintf("mapper %s has no function %s", meta.rule.mapperPkg, meta.rule.mapperFunc),
			Span:    report.Span{Offset: impOff, Length: max(impLen, 1), Line: impLine, Column: impCol},
		})
		return
	}

	mediaKind := strings.TrimPrefix(pathpkg.Ext(meta.relPath), ".")
	if mediaKind == "" {
		mediaKind = "asset"
	}

	res := contentmap.RunFileAsset(contentmap.FileAssetRequest{
		Target:       syntax.AssetStem(meta.relPath),
		AssetPath:    l.relIn(p.mod, meta.absPath),
		PackagePath:  p.path,
		MediaKind:    mediaKind,
		Bytes:        meta.bytes,
		Companions:   meta.companions,
		MapperPkg:    mapperPkg,
		MapperFn:     mapperFn,
		Cache:        l.opts.MapperCache,
		BuildID:      l.opts.MapperBuildID,
		ImportFile:   meta.importFile.rel,
		ImportOffset: impOff,
		ImportLength: impLen,
		ImportLine:   impLine,
		ImportCol:    impCol,
	})
	rec := res.Record
	p.assetRecord = &rec
	if res.Broken || res.GenFile == nil {
		p.broken = true
		for _, ad := range res.Diags {
			l.diags = append(l.diags, report.Diagnostic{
				Code:    ad.Code,
				File:    ad.File,
				Message: ad.Message,
				Span:    report.Span{Offset: ad.Offset, Length: ad.Length, Line: ad.Line, Column: ad.Column},
			})
		}
		return
	}

	p.files = []loadedFile{{
		rel:    meta.relPath + ".ergo",
		abs:    meta.absPath + ".ergo",
		text:   res.Source,
		syntax: res.GenFile,
	}}
	if l.cache != nil {
		if ckey, cacheable := l.cache.key(l, p, p.path); cacheable {
			if pkg, ok := l.cache.reuse(l, p, ckey); ok {
				pkg.Name = syntax.AssetStem(meta.relPath)
				if pkg.Derive == nil {
					pkg.Derive = &check.DeriveRecord{}
				}
				if len(pkg.Derive.Records) == 0 {
					pkg.Derive.Records = []check.MapperRecord{rec}
				}
				p.pkg = pkg
				stage.Add(stage.PackagesReused, 1)
				return
			}
		}
	}
	pkg, found := check.CheckTestOptions(p.path, []*syntax.File{res.GenFile}, l, nil, l.opts)
	stage.Add(stage.PackagesChecked, 1)
	if len(found) > 0 {
		p.broken = true
		for _, d := range found {
			ad := res.RemapDiag(d)
			l.diags = append(l.diags, report.Diagnostic{
				Code:    ad.Code,
				File:    ad.File,
				Message: ad.Message,
				Span:    report.Span{Offset: ad.Offset, Length: ad.Length, Line: ad.Line, Column: ad.Column},
			})
		}
		return
	}
	if pkg.Derive == nil {
		pkg.Derive = &check.DeriveRecord{}
	}
	pkg.Name = syntax.AssetStem(meta.relPath)
	pkg.Derive.Records = append(pkg.Derive.Records, rec)
	p.pkg = pkg
}
