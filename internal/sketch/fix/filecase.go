package fix

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/lint"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/sketchfront"
	sourcefile "ergo.local/prototype/internal/source"
)

// ConfigError wraps an invalid lint configuration encountered during auto-fix.
type ConfigError struct {
	Err error
}

func (e *ConfigError) Error() string { return e.Err.Error() }
func (e *ConfigError) Unwrap() error { return e.Err }

// FileCaseOptions controls deterministic, collision-safe file-name-case renames.
type FileCaseOptions struct {
	Paths  []string
	Write  bool
	Config *lint.Configuration
}

// FileCaseRename describes one planned or applied file or directory rename.
type FileCaseRename struct {
	Kind string `json:"kind"`
	From string `json:"from"`
	To   string `json:"to"`
}

// FileCaseImportEdit describes an intra-repo import path update caused by a directory rename.
type FileCaseImportEdit struct {
	File string `json:"file"`
	From string `json:"from"`
	To   string `json:"to"`
}

// FileCaseSkip describes a candidate rename skipped due to a collision or conflict.
type FileCaseSkip struct {
	Kind   string `json:"kind"`
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason"`
}

// FileCaseReport summarizes planned or applied renames, import updates, and skipped conflicts.
type FileCaseReport struct {
	Renames       []FileCaseRename     `json:"renames,omitempty"`
	ImportUpdates []FileCaseImportEdit `json:"importUpdates,omitempty"`
	Skipped       []FileCaseSkip       `json:"skipped,omitempty"`
}

type fileCaseTarget struct {
	abs   string
	isDir bool
}

type fileCaseScope struct {
	root    string
	hasMod  bool
	modPath string
	targets []fileCaseTarget
}

type textEdit struct {
	start int
	end   int
	text  string
}

type packageInfo struct {
	relDir string
	path   string
	pkg    *check.Package
	files  map[string]*syntax.File
}

// RunFileCase detects and optionally applies deterministic, collision-safe
// renames for files (and directories when dirs=true) configured by the active
// file-name-case lint rule.
func RunFileCase(opts FileCaseOptions) (FileCaseReport, error) {
	var rep FileCaseReport
	if len(opts.Paths) == 0 {
		return rep, nil
	}
	scopes, err := collectFileCaseScopes(opts.Paths)
	if err != nil {
		return rep, err
	}
	for _, sc := range scopes {
		sub, err := runFileCaseScope(sc, opts)
		if err != nil {
			return rep, err
		}
		rep.Renames = append(rep.Renames, sub.Renames...)
		rep.ImportUpdates = append(rep.ImportUpdates, sub.ImportUpdates...)
		rep.Skipped = append(rep.Skipped, sub.Skipped...)
	}
	return rep, nil
}

func collectFileCaseScopes(rawPaths []string) ([]*fileCaseScope, error) {
	byRoot := map[string]*fileCaseScope{}
	var roots []string
	for _, raw := range rawPaths {
		p := strings.TrimSuffix(raw, "/...")
		if p == "..." {
			p = "."
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(abs)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		startDir := abs
		if !info.IsDir() {
			startDir = filepath.Dir(abs)
		}
		modRoot, modPath, hasMod, err := findModuleUpward(startDir)
		if err != nil {
			return nil, err
		}
		scopeRoot := startDir
		if hasMod {
			scopeRoot = modRoot
		}
		sc := byRoot[scopeRoot]
		if sc == nil {
			sc = &fileCaseScope{root: scopeRoot, hasMod: hasMod, modPath: modPath}
			byRoot[scopeRoot] = sc
			roots = append(roots, scopeRoot)
		}
		sc.targets = append(sc.targets, fileCaseTarget{abs: abs, isDir: info.IsDir()})
	}
	slices.Sort(roots)
	out := make([]*fileCaseScope, 0, len(roots))
	for _, r := range roots {
		out = append(out, byRoot[r])
	}
	return out, nil
}

func findModuleUpward(dir string) (root, modPath string, ok bool, err error) {
	cur := dir
	for {
		modFile := filepath.Join(cur, sketchfront.ModFile)
		info, statErr := os.Stat(modFile)
		if statErr == nil && !info.IsDir() {
			data, rerr := os.ReadFile(modFile)
			if rerr != nil {
				return "", "", false, rerr
			}
			mp := parseModulePath(string(data))
			return cur, mp, true, nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", "", false, nil
		}
		cur = parent
	}
}

func parseModulePath(src string) string {
	for _, line := range strings.Split(src, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "module" {
			return strings.Trim(fields[1], `"`)
		}
	}
	return ""
}

func loadScopeLintConfig(sc *fileCaseScope, override *lint.Configuration) (*lint.Configuration, error) {
	if override != nil {
		return override, nil
	}
	if !sc.hasMod {
		return lint.Builtin().Configure(lint.DefaultConfig(), nil)
	}
	cfgPath := filepath.Join(sc.root, lint.ConfigFile)
	data, err := sourcefile.ReadRegularFile(cfgPath, lint.MaxConfigBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return lint.Builtin().Configure(lint.DefaultConfig(), nil)
	}
	if err != nil {
		return nil, &ConfigError{Err: err}
	}
	doc, err := lint.ParseConfig(data)
	if err != nil {
		return nil, &ConfigError{Err: fmt.Errorf("%s: %w", lint.ConfigFile, err)}
	}
	conf, err := lint.Builtin().Configure(doc.Builtins(), nil)
	if err != nil {
		return nil, &ConfigError{Err: fmt.Errorf("%s: %w", lint.ConfigFile, err)}
	}
	return conf, nil
}

func runFileCaseScope(sc *fileCaseScope, opts FileCaseOptions) (FileCaseReport, error) {
	var rep FileCaseReport
	conf, err := loadScopeLintConfig(sc, opts.Config)
	if err != nil {
		return rep, err
	}
	caseOpts, active := conf.FileNameCase()
	if !active {
		return rep, nil
	}

	filesRel, dirsRel, pkgByRelDir, suppressed, fileTexts, err := walkAndInspectScope(sc, conf)
	if err != nil {
		return rep, err
	}
	if len(filesRel) == 0 {
		return rep, nil
	}

	inTargetDir := map[string]bool{}
	inTargetFile := map[string]bool{}
	for _, d := range dirsRel {
		absDir := filepath.Join(sc.root, filepath.FromSlash(d))
		if targetCoversDir(sc.targets, absDir) {
			inTargetDir[d] = true
		}
	}
	for _, f := range filesRel {
		absFile := filepath.Join(sc.root, filepath.FromSlash(f))
		if targetCoversFile(sc.targets, absFile) {
			inTargetFile[f] = true
		}
	}

	// Step A: Plan directory renames when dirs=true.
	effectiveDir := map[string]string{".": "."}
	var approvedDirMoves []dirMove
	if caseOpts.Dirs && len(dirsRel) > 0 {
		effectiveDir, approvedDirMoves, rep.Skipped = planDirectoryRenames(sc, caseOpts, dirsRel, inTargetDir, pkgByRelDir, fileTexts, suppressed)
	} else {
		for _, d := range dirsRel {
			effectiveDir[d] = d
		}
	}

	// Step B: Plan intra-repo import edits for moved packages.
	var plannedEdits map[string][]textEdit
	var plannedImportUpdates []FileCaseImportEdit
	if len(approvedDirMoves) > 0 {
		for {
			conflictDir := ""
			plannedEdits, plannedImportUpdates, conflictDir = planModuleImportEdits(sc, effectiveDir, pkgByRelDir, fileTexts)
			if conflictDir == "" {
				break
			}
			// Revert conflicting directory move and re-plan without it.
			rep.Skipped = append(rep.Skipped, FileCaseSkip{
				Kind:   "dir",
				From:   filepath.Join(sc.root, filepath.FromSlash(conflictDir)),
				To:     filepath.Join(sc.root, filepath.FromSlash(effectiveDir[conflictDir])),
				Reason: "renamed package qualifier collides with a local declaration in caller",
			})
			inTargetDir[conflictDir] = false
			var extraSkips []FileCaseSkip
			effectiveDir, approvedDirMoves, extraSkips = planDirectoryRenames(sc, caseOpts, dirsRel, inTargetDir, pkgByRelDir, fileTexts, suppressed)
			rep.Skipped = append(rep.Skipped, extraSkips...)
		}
	}

	// Step C: Plan file renames within finalized effective directories.
	approvedFileMoves, fileSkips := planFileRenames(sc, caseOpts, filesRel, inTargetFile, effectiveDir, suppressed)
	rep.Skipped = append(rep.Skipped, fileSkips...)

	// Record directory renames, then file renames in deterministic sorted order.
	for _, dm := range approvedDirMoves {
		rep.Renames = append(rep.Renames, FileCaseRename{
			Kind: "dir",
			From: filepath.Join(sc.root, filepath.FromSlash(dm.origRel)),
			To:   filepath.Join(sc.root, filepath.FromSlash(dm.effRel)),
		})
	}
	for _, fm := range approvedFileMoves {
		rep.Renames = append(rep.Renames, FileCaseRename{
			Kind: "file",
			From: filepath.Join(sc.root, filepath.FromSlash(fm.origRel)),
			To:   filepath.Join(sc.root, filepath.FromSlash(fm.finalRel)),
		})
	}
	rep.ImportUpdates = plannedImportUpdates

	if opts.Write {
		if err := applyFileCasePlan(sc.root, approvedDirMoves, approvedFileMoves, effectiveDir, plannedEdits, fileTexts); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

type dirMove struct {
	origRel string
	effRel  string
}

type fileMove struct {
	origRel  string
	effDir   string
	origBase string
	wantBase string
	finalRel string
}

func targetCoversDir(targets []fileCaseTarget, absDir string) bool {
	cleanDir := filepath.Clean(absDir)
	for _, t := range targets {
		if !t.isDir {
			continue
		}
		cleanT := filepath.Clean(t.abs)
		if cleanDir == cleanT || strings.HasPrefix(cleanDir, cleanT+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func targetCoversFile(targets []fileCaseTarget, absFile string) bool {
	cleanFile := filepath.Clean(absFile)
	for _, t := range targets {
		cleanT := filepath.Clean(t.abs)
		if !t.isDir {
			if cleanFile == cleanT {
				return true
			}
		} else if strings.HasPrefix(cleanFile, cleanT+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func walkAndInspectScope(sc *fileCaseScope, conf *lint.Configuration) (
	filesRel []string,
	dirsRel []string,
	pkgByRelDir map[string]*packageInfo,
	suppressed map[string]bool,
	fileTexts map[string]string,
	err error,
) {
	dirSet := map[string]bool{}
	fileTexts = map[string]string{}
	suppressed = map[string]bool{}
	pkgByRelDir = map[string]*packageInfo{}

	err = filepath.WalkDir(sc.root, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.Type()&os.ModeSymlink != 0 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if p != sc.root {
				if !sketchfront.AdmitFolder(name) || name == "gostd" {
					return filepath.SkipDir
				}
				if _, modErr := os.Stat(filepath.Join(p, sketchfront.ModFile)); modErr == nil {
					return filepath.SkipDir
				}
			}
			return nil
		}
		name := d.Name()
		if !d.Type().IsRegular() || !strings.HasSuffix(name, ".ergo") || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			return nil
		}
		rel, rerr := filepath.Rel(sc.root, p)
		if rerr != nil {
			return rerr
		}
		rel = path.Clean(filepath.ToSlash(rel))
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		filesRel = append(filesRel, rel)
		fileTexts[rel] = string(data)
		for cur := path.Dir(rel); cur != "." && cur != ""; cur = path.Dir(cur) {
			dirSet[cur] = true
		}
		return nil
	})
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	slices.Sort(filesRel)
	for d := range dirSet {
		dirsRel = append(dirsRel, d)
	}
	slices.SortFunc(dirsRel, func(a, b string) int {
		da := strings.Count(a, "/")
		db := strings.Count(b, "/")
		if da != db {
			return cmp.Compare(da, db)
		}
		return strings.Compare(a, b)
	})

	// Check packages to resolve imports/uses for import rewriting and suppression evaluation.
	if err := inspectPackages(sc, conf, filesRel, fileTexts, pkgByRelDir, suppressed); err != nil {
		return nil, nil, nil, nil, nil, err
	}
	return filesRel, dirsRel, pkgByRelDir, suppressed, fileTexts, nil
}

func inspectPackages(
	sc *fileCaseScope,
	conf *lint.Configuration,
	filesRel []string,
	fileTexts map[string]string,
	pkgByRelDir map[string]*packageInfo,
	suppressed map[string]bool,
) error {
	pkgDirsSet := map[string]bool{}
	for _, rel := range filesRel {
		pkgDirsSet[path.Dir(rel)] = true
	}
	var pkgRelDirs []string
	for d := range pkgDirsSet {
		pkgRelDirs = append(pkgRelDirs, d)
	}
	// Check deeper/consumer packages first or use DiscoverPackages order reversed.
	if sc.hasMod {
		targets, err := sketchfront.DiscoverPackages(sc.root)
		if err == nil && len(targets) > 0 {
			checked := map[string]bool{}
			for i := len(targets) - 1; i >= 0; i-- {
				t := targets[i]
				if checked[t.Dir] {
					continue
				}
				a, cerr := sketchfront.CheckDir(t.Dir)
				if cerr != nil {
					continue
				}
				recordCheckedAnalysis(sc, conf, a, pkgByRelDir, suppressed, checked)
			}
		}
	}
	for _, d := range pkgRelDirs {
		if pkgByRelDir[d] != nil {
			continue
		}
		absDir := sc.root
		if d != "." {
			absDir = filepath.Join(sc.root, filepath.FromSlash(d))
		}
		a, cerr := sketchfront.CheckDir(absDir)
		if cerr == nil {
			recordCheckedAnalysis(sc, conf, a, pkgByRelDir, suppressed, nil)
		}
		if pkgByRelDir[d] == nil {
			// Fallback to syntax-only parse when package is incomplete/broken.
			pi := &packageInfo{relDir: d, files: map[string]*syntax.File{}}
			for _, rel := range filesRel {
				if path.Dir(rel) == d {
					if sf, perr := syntax.Parse(fileTexts[rel]); perr == nil {
						pi.files[rel] = sf
					}
				}
			}
			pkgByRelDir[d] = pi
		}
	}
	return nil
}

func recordCheckedAnalysis(
	sc *fileCaseScope,
	conf *lint.Configuration,
	a sketchfront.Analysis,
	pkgByRelDir map[string]*packageInfo,
	suppressed map[string]bool,
	checked map[string]bool,
) {
	in := lint.InputOf(a)
	if in.Checked {
		res := conf.Run(in)
		for _, s := range res.Suppressions {
			if s.Rule == "file-name-case" && s.Status == lint.Applied && s.File != "" {
				suppressed[path.Clean(s.File)] = true
			}
		}
	}
	for _, cp := range a.Packages() {
		if cp.Module != "" {
			continue
		}
		if checked != nil {
			checked[cp.Dir] = true
		}
		relDir, err := filepath.Rel(sc.root, cp.Dir)
		if err != nil {
			continue
		}
		relDir = path.Clean(filepath.ToSlash(relDir))
		if pkgByRelDir[relDir] != nil && pkgByRelDir[relDir].pkg != nil {
			continue
		}
		pkg := cp.Package
		if pkg == nil {
			pkg = cp.Partial
		}
		pi := &packageInfo{relDir: relDir, path: cp.Path, pkg: pkg, files: map[string]*syntax.File{}}
		for _, f := range cp.Files {
			if f.Syntax != nil && f.Rel != "" {
				pi.files[path.Clean(f.Rel)] = f.Syntax
			}
		}
		pkgByRelDir[relDir] = pi
	}
}

func planDirectoryRenames(
	sc *fileCaseScope,
	caseOpts lint.FileNameCaseOptions,
	dirsRel []string,
	inTargetDir map[string]bool,
	pkgByRelDir map[string]*packageInfo,
	fileTexts map[string]string,
	suppressed map[string]bool,
) (effectiveDir map[string]string, moves []dirMove, skips []FileCaseSkip) {
	effectiveDir = map[string]string{".": "."}
	byDepth := map[int][]string{}
	maxDepth := 0
	for _, d := range dirsRel {
		depth := strings.Count(d, "/") + 1
		byDepth[depth] = append(byDepth[depth], d)
		if depth > maxDepth {
			maxDepth = depth
		}
	}

	dirSuppressed := func(dirRel string) bool {
		// If every file directly in dirRel is suppressed, or anchor file is suppressed, keep dir.
		hasDirect := false
		for rel := range fileTexts {
			if path.Dir(rel) == dirRel {
				hasDirect = true
				if suppressed[rel] {
					return true
				}
			}
		}
		_ = hasDirect
		return false
	}

	joinRel := func(parent, seg string) string {
		if parent == "." || parent == "" {
			return seg
		}
		return parent + "/" + seg
	}

	for depth := 1; depth <= maxDepth; depth++ {
		ds := byDepth[depth]
		slices.Sort(ds)
		desired := map[string]string{}
		stays := map[string]bool{}
		origInfos := map[string]os.FileInfo{}
		trackedChildByParentSeg := map[string]string{}

		for _, d := range ds {
			parent := path.Dir(d)
			effParent := effectiveDir[parent]
			seg := path.Base(d)
			trackedChildByParentSeg[parent+"\x00"+seg] = d
			absOrig := filepath.Join(sc.root, filepath.FromSlash(d))
			if info, err := os.Lstat(absOrig); err == nil {
				origInfos[d] = info
			}

			wantSeg, canNorm := lint.NormalizeDirSegment(seg, caseOpts.Case)
			if !inTargetDir[d] || !canNorm || wantSeg == seg || dirSuppressed(d) {
				stays[d] = true
				desired[d] = joinRel(effParent, seg)
			} else {
				desired[d] = joinRel(effParent, wantSeg)
			}
		}

		// Fixed-point collision resolution within this depth.
		for {
			changed := false
			claims := map[string]int{}
			for _, d := range ds {
				claims[desired[d]]++
			}
			for _, d := range ds {
				if stays[d] {
					continue
				}
				parent := path.Dir(d)
				effParent := effectiveDir[parent]
				seg := path.Base(d)
				targetRel := desired[d]
				targetSeg := path.Base(targetRel)

				reason := ""
				if claims[targetRel] > 1 {
					reason = fmt.Sprintf("target directory collision: multiple directories map to %s", targetRel)
				} else {
					// Check disk for untracked sibling or case-insensitive non-SameFile target.
					parentAbs := sc.root
					if parent != "." {
						parentAbs = filepath.Join(sc.root, filepath.FromSlash(parent))
					}
					targetAbsCurrent := filepath.Join(parentAbs, targetSeg)
					if tInfo, lerr := os.Lstat(targetAbsCurrent); lerr == nil {
						if !os.SameFile(origInfos[d], tInfo) {
							otherD, isTracked := trackedChildByParentSeg[parent+"\x00"+targetSeg]
							if !isTracked || stays[otherD] || desired[otherD] == joinRel(effParent, targetSeg) {
								reason = fmt.Sprintf("target directory %s already exists", targetRel)
							}
						}
					}
				}
				if reason != "" {
					stays[d] = true
					desired[d] = joinRel(effParent, seg)
					skips = append(skips, FileCaseSkip{
						Kind:   "dir",
						From:   filepath.Join(sc.root, filepath.FromSlash(d)),
						To:     filepath.Join(sc.root, filepath.FromSlash(targetRel)),
						Reason: reason,
					})
					changed = true
				}
			}
			if !changed {
				break
			}
		}

		for _, d := range ds {
			effectiveDir[d] = desired[d]
			if !stays[d] {
				moves = append(moves, dirMove{origRel: d, effRel: desired[d]})
			}
		}
	}
	return effectiveDir, moves, skips
}

func planModuleImportEdits(
	sc *fileCaseScope,
	effectiveDir map[string]string,
	pkgByRelDir map[string]*packageInfo,
	fileTexts map[string]string,
) (editsByFile map[string][]textEdit, updates []FileCaseImportEdit, conflictDir string) {
	if !sc.hasMod || sc.modPath == "" {
		return nil, nil, ""
	}
	pkgRenames := map[string]string{}
	oldDirByImp := map[string]string{}
	for origDir := range pkgByRelDir {
		effDir, ok := effectiveDir[origDir]
		if !ok || effDir == origDir || origDir == "." {
			continue
		}
		oldImp := sc.modPath + "/" + origDir
		newImp := sc.modPath + "/" + effDir
		pkgRenames[oldImp] = newImp
		oldDirByImp[oldImp] = origDir
	}
	if len(pkgRenames) == 0 {
		return nil, nil, ""
	}

	editsByFile = map[string][]textEdit{}
	// Deterministic package/file ordering.
	var dirs []string
	for d := range pkgByRelDir {
		dirs = append(dirs, d)
	}
	slices.Sort(dirs)

	for _, d := range dirs {
		pi := pkgByRelDir[d]
		var files []string
		for rel := range pi.files {
			files = append(files, rel)
		}
		slices.Sort(files)
		for _, rel := range files {
			sf := pi.files[rel]
			src := fileTexts[rel]
			fileEdits, fileUpdates, badImp := planFileImportEdits(rel, src, sf, pi, pkgRenames)
			if badImp != "" {
				return nil, nil, oldDirByImp[badImp]
			}
			if len(fileEdits) > 0 {
				editsByFile[rel] = append(editsByFile[rel], fileEdits...)
				updates = append(updates, fileUpdates...)
			}
		}
	}
	return editsByFile, updates, ""
}

func planFileImportEdits(
	rel string,
	src string,
	sf *syntax.File,
	pi *packageInfo,
	pkgRenames map[string]string,
) (edits []textEdit, updates []FileCaseImportEdit, conflictImp string) {
	if sf == nil || pi == nil {
		return nil, nil, ""
	}
	pkg := pi.pkg
	// Compute resulting import default/bound names per file to detect clashes after renames.
	type impEntry struct {
		decl    *syntax.ImportDecl
		spec    *syntax.ImportSpec
		oldImp  string
		newImp  string
		changed bool
	}
	var imps []impEntry
	postNames := map[string]int{}
	for _, d := range sf.Decls {
		id, ok := d.(*syntax.ImportDecl)
		if !ok || len(id.Specs) == 0 {
			continue
		}
		s := id.Specs[0]
		if s.Path == nil {
			continue
		}
		oldPath := importPathValue(s.Path)
		newPath, ch := pkgRenames[oldPath]
		if !ch {
			newPath = oldPath
		}
		imps = append(imps, impEntry{decl: id, spec: s, oldImp: oldPath, newImp: newPath, changed: ch})
		if s.Kind == syntax.ImportGo || s.Kind == syntax.ImportDot {
			continue
		}
		if s.Kind == syntax.ImportNamed && s.Name != nil {
			postNames[s.Name.Name]++
		} else {
			postNames[path.Base(newPath)]++
		}
	}

	var info *check.Info
	if pkg != nil {
		info = pkg.Info
	}

	pkgTopNames := map[string]bool{}
	for _, pf := range pi.files {
		collectPkgTopNames(pf, pkgTopNames)
	}
	pkgLevelClashes := func(name string) bool {
		if pkgTopNames[name] {
			return true
		}
		if pkg != nil && pkg.Contexts != nil && pkg.Contexts[name] != nil {
			return true
		}
		return false
	}

	// Find any *check.PkgName for each ImportSpec in this file.
	pkgNameForSpec := map[*syntax.ImportSpec]*check.PkgName{}
	if info != nil {
		for _, id := range sf.Decls {
			impDecl, ok := id.(*syntax.ImportDecl)
			if !ok || len(impDecl.Specs) == 0 {
				continue
			}
			spec := impDecl.Specs[0]
			for _, obj := range info.Uses {
				if pn, ok := obj.(*check.PkgName); ok && pn.Pkg != nil && pn.Pkg.Path == importPathValue(spec.Path) {
					if spec.Kind == syntax.ImportNamed && spec.Name != nil && pn.Name != spec.Name.Name {
						continue
					}
					if spec.Kind == syntax.ImportPlain && pn.Name != path.Base(importPathValue(spec.Path)) {
						continue
					}
					pkgNameForSpec[spec] = pn
					break
				}
			}
		}
	}

	for _, im := range imps {
		if !im.changed {
			continue
		}
		s := im.spec
		oldBase := path.Base(im.oldImp)
		newBase := path.Base(im.newImp)
		pathText := formatImportPathReplacement(s.Path, im.newImp)
		updates = append(updates, FileCaseImportEdit{File: rel, From: im.oldImp, To: im.newImp})

		switch s.Kind {
		case syntax.ImportGo, syntax.ImportDot:
			edits = append(edits, textEdit{start: int(s.Path.Pos()), end: int(s.Path.End()), text: pathText})
		case syntax.ImportNamed:
			// Check whether the existing alias is still valid/needed after rename.
			alias := ""
			if s.Name != nil {
				alias = s.Name.Name
			}
			needsAlias := !isValidErgoIdent(newBase) || syntax.IsKeyword(newBase) || pkgLevelClashes(newBase) || postNames[newBase] > 0
			if needsAlias || alias == "" || alias == newBase {
				edits = append(edits, textEdit{start: int(s.Path.Pos()), end: int(s.Path.End()), text: pathText})
			} else {
				// Existing alias is no longer allowed by decl.go; drop alias and update uses if safe.
				pn := pkgNameForSpec[s]
				if hasLocalBindingCollision(sf, info, pn, newBase) {
					return nil, nil, im.oldImp
				}
				postNames[newBase]++
				edits = append(edits, textEdit{start: int(s.Name.Pos()), end: int(s.Path.End()), text: pathText})
				edits = append(edits, rewritePkgIdentUses(sf, info, pn, alias, newBase)...)
			}
		case syntax.ImportPlain:
			if oldBase == newBase {
				edits = append(edits, textEdit{start: int(s.Path.Pos()), end: int(s.Path.End()), text: pathText})
				continue
			}
			// Check if newBase requires an explicit alias per decl.go checkImportAliases.
			otherClash := postNames[newBase] > 1
			needsAlias := !isValidErgoIdent(newBase) || syntax.IsKeyword(newBase) || pkgLevelClashes(newBase) || otherClash
			if needsAlias {
				if !isValidErgoIdent(oldBase) || syntax.IsKeyword(oldBase) {
					return nil, nil, im.oldImp
				}
				edits = append(edits, textEdit{start: int(s.Path.Pos()), end: int(s.Path.End()), text: oldBase + " " + pathText})
				continue
			}
			pn := pkgNameForSpec[s]
			if hasLocalBindingCollision(sf, info, pn, newBase) {
				return nil, nil, im.oldImp
			}
			edits = append(edits, textEdit{start: int(s.Path.Pos()), end: int(s.Path.End()), text: pathText})
			edits = append(edits, rewritePkgIdentUses(sf, info, pn, oldBase, newBase)...)
		}
	}
	return edits, updates, ""
}

func collectPkgTopNames(sf *syntax.File, names map[string]bool) {
	if sf == nil {
		return
	}
	for _, d := range sf.Decls {
		switch d := d.(type) {
		case *syntax.FuncDecl:
			if d.Name != nil {
				names[d.Name.Name] = true
			}
		case *syntax.TypeDecl:
			if d.Name != nil {
				names[d.Name.Name] = true
			}
		case *syntax.ContextDecl:
			if d.Name != nil {
				names[d.Name.Name] = true
			}
		case *syntax.LawDecl:
			if d.Name != nil {
				names[d.Name.Name] = true
			}
		case *syntax.VarDecl:
			for _, vs := range d.Specs {
				for _, id := range vs.Names {
					names[id.Name] = true
				}
			}
		}
	}
}

func importPathValue(lit *syntax.BasicLit) string {
	if lit == nil {
		return ""
	}
	if lit.Kind == syntax.STRING {
		if u, err := strconv.Unquote(lit.Value); err == nil {
			return u
		}
		return strings.Trim(lit.Value, `"`)
	}
	return lit.Value
}

func formatImportPathReplacement(lit *syntax.BasicLit, newPath string) string {
	if lit != nil && lit.Kind == syntax.STRING {
		return strconv.Quote(newPath)
	}
	return newPath
}

func isValidErgoIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if i == 0 {
			if r != '_' && !unicode.IsLetter(r) {
				return false
			}
		} else if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func hasLocalBindingCollision(sf *syntax.File, info *check.Info, pn *check.PkgName, newName string) bool {
	if info == nil || pn == nil {
		return false
	}
	for _, decl := range sf.Decls {
		switch decl.(type) {
		case *syntax.FuncDecl, *syntax.TestDecl, *syntax.LawDecl:
		default:
			continue
		}
		usesPN := false
		shadows := false
		for id, obj := range info.Uses {
			if obj == pn && id.Pos() >= decl.Pos() && id.Pos() <= decl.End() {
				usesPN = true
				break
			}
		}
		if !usesPN {
			continue
		}
		for id, obj := range info.Defs {
			if id == nil || id.Name != newName || id.Pos() < decl.Pos() || id.Pos() > decl.End() {
				continue
			}
			if _, ok := obj.(*check.Var); ok {
				shadows = true
				break
			}
		}
		if shadows {
			return true
		}
	}
	return false
}

func rewritePkgIdentUses(sf *syntax.File, info *check.Info, pn *check.PkgName, oldQual, newQual string) []textEdit {
	var out []textEdit
	importEnd := syntax.Pos(0)
	for _, d := range sf.Decls {
		if _, ok := d.(*syntax.ImportDecl); ok && d.End() > importEnd {
			importEnd = d.End()
		}
	}
	seenPos := map[syntax.Pos]bool{}
	if info != nil && pn != nil {
		for id, obj := range info.Uses {
			if obj != pn || id.Pos() < sf.Pos() || id.Pos() > sf.EOF || id.Pos() <= importEnd {
				continue
			}
			if seenPos[id.Pos()] || id.Name != oldQual {
				continue
			}
			seenPos[id.Pos()] = true
			out = append(out, textEdit{start: int(id.Pos()), end: int(id.End()), text: newQual})
		}
		return out
	}
	syntax.Inspect(sf, func(n syntax.Node) bool {
		if n == nil {
			return false
		}
		if _, isImp := n.(*syntax.ImportDecl); isImp {
			return false
		}
		if sel, ok := n.(*syntax.SelectorExpr); ok {
			if id, ok := sel.X.(*syntax.Ident); ok && id.Name == oldQual && !seenPos[id.Pos()] {
				seenPos[id.Pos()] = true
				out = append(out, textEdit{start: int(id.Pos()), end: int(id.End()), text: newQual})
			}
		}
		return true
	})
	return out
}

func planFileRenames(
	sc *fileCaseScope,
	caseOpts lint.FileNameCaseOptions,
	filesRel []string,
	inTargetFile map[string]bool,
	effectiveDir map[string]string,
	suppressed map[string]bool,
) (moves []fileMove, skips []FileCaseSkip) {
	byOrigDir := map[string][]string{}
	var dirs []string
	for _, rel := range filesRel {
		d := path.Dir(rel)
		if _, ok := byOrigDir[d]; !ok {
			dirs = append(dirs, d)
		}
		byOrigDir[d] = append(byOrigDir[d], rel)
	}
	slices.Sort(dirs)

	for _, origDir := range dirs {
		effDir := effectiveDir[origDir]
		if effDir == "" {
			effDir = origDir
		}
		fsRel := byOrigDir[origDir]
		slices.Sort(fsRel)

		origAbsDir := sc.root
		if origDir != "." {
			origAbsDir = filepath.Join(sc.root, filepath.FromSlash(origDir))
		}

		desiredBase := map[string]string{}
		stays := map[string]bool{}
		origInfos := map[string]os.FileInfo{}
		trackedByBase := map[string]string{}

		for _, rel := range fsRel {
			base := path.Base(rel)
			trackedByBase[base] = rel
			absOrig := filepath.Join(origAbsDir, base)
			if info, err := os.Lstat(absOrig); err == nil {
				origInfos[rel] = info
			}
			wantBase, ok := lint.NormalizeErgoFileName(base, caseOpts.Case)
			if !inTargetFile[rel] || suppressed[rel] || !ok || wantBase == base {
				stays[rel] = true
				desiredBase[rel] = base
			} else {
				desiredBase[rel] = wantBase
			}
		}

		// Fixed-point collision resolution within origDir.
		for {
			changed := false
			claims := map[string]int{}
			for _, rel := range fsRel {
				claims[desiredBase[rel]]++
			}
			for _, rel := range fsRel {
				if stays[rel] {
					continue
				}
				base := path.Base(rel)
				wantBase := desiredBase[rel]
				reason := ""
				if claims[wantBase] > 1 {
					reason = fmt.Sprintf("target collision: multiple files map to %s", path.Join(effDir, wantBase))
				} else {
					targetAbs := filepath.Join(origAbsDir, wantBase)
					if tInfo, lerr := os.Lstat(targetAbs); lerr == nil {
						if !os.SameFile(origInfos[rel], tInfo) {
							otherRel, isTracked := trackedByBase[wantBase]
							if !isTracked || stays[otherRel] || desiredBase[otherRel] == wantBase {
								reason = fmt.Sprintf("target collision: %s already exists", path.Join(effDir, wantBase))
							}
						}
					}
				}
				if reason != "" {
					stays[rel] = true
					desiredBase[rel] = base
					skips = append(skips, FileCaseSkip{
						Kind:   "file",
						From:   filepath.Join(sc.root, filepath.FromSlash(rel)),
						To:     filepath.Join(sc.root, filepath.FromSlash(path.Join(effDir, wantBase))),
						Reason: reason,
					})
					changed = true
				}
			}
			if !changed {
				break
			}
		}

		for _, rel := range fsRel {
			if !stays[rel] {
				moves = append(moves, fileMove{
					origRel:  rel,
					effDir:   effDir,
					origBase: path.Base(rel),
					wantBase: desiredBase[rel],
					finalRel: path.Join(effDir, desiredBase[rel]),
				})
			}
		}
	}
	return moves, skips
}

func applyFileCasePlan(
	scopeRoot string,
	dirMoves []dirMove,
	fileMoves []fileMove,
	effectiveDir map[string]string,
	editsByFile map[string][]textEdit,
	fileTexts map[string]string,
) error {
	// 1. Directory renames: deepest first, grouped by current parent dir via two-phase temp renames.
	type parentGroupKey struct {
		parentRel string
		depth     int
	}
	groupsMap := map[parentGroupKey][]dirMove{}
	var groupKeys []parentGroupKey
	for _, dm := range dirMoves {
		k := parentGroupKey{parentRel: path.Dir(dm.origRel), depth: strings.Count(dm.origRel, "/") + 1}
		if _, ok := groupsMap[k]; !ok {
			groupKeys = append(groupKeys, k)
		}
		groupsMap[k] = append(groupsMap[k], dm)
	}
	slices.SortFunc(groupKeys, func(a, b parentGroupKey) int {
		if a.depth != b.depth {
			return cmp.Compare(b.depth, a.depth) // deepest first
		}
		return strings.Compare(a.parentRel, b.parentRel)
	})
	for _, k := range groupKeys {
		parentAbs := scopeRoot
		if k.parentRel != "." {
			parentAbs = filepath.Join(scopeRoot, filepath.FromSlash(k.parentRel))
		}
		ms := groupsMap[k]
		if err := twoPhaseRenameDirs(parentAbs, ms); err != nil {
			return err
		}
	}

	// 2. File renames: now inside finalized effDir via two-phase temp renames.
	fileGroups := map[string][]fileMove{}
	var effDirs []string
	for _, fm := range fileMoves {
		if _, ok := fileGroups[fm.effDir]; !ok {
			effDirs = append(effDirs, fm.effDir)
		}
		fileGroups[fm.effDir] = append(fileGroups[fm.effDir], fm)
	}
	slices.Sort(effDirs)
	for _, ed := range effDirs {
		absDir := scopeRoot
		if ed != "." {
			absDir = filepath.Join(scopeRoot, filepath.FromSlash(ed))
		}
		if err := twoPhaseRenameFiles(absDir, fileGroups[ed]); err != nil {
			return err
		}
	}

	// 3. Import edits: write updated text to each file's finalized location.
	movedBase := map[string]string{}
	for _, fm := range fileMoves {
		movedBase[fm.origRel] = fm.wantBase
	}
	var editedFiles []string
	for rel, es := range editsByFile {
		if len(es) > 0 {
			editedFiles = append(editedFiles, rel)
		}
	}
	slices.Sort(editedFiles)
	for _, origRel := range editedFiles {
		effDir := effectiveDir[path.Dir(origRel)]
		if effDir == "" {
			effDir = path.Dir(origRel)
		}
		base := path.Base(origRel)
		if b, ok := movedBase[origRel]; ok {
			base = b
		}
		finalRel := path.Join(effDir, base)
		finalAbs := filepath.Join(scopeRoot, filepath.FromSlash(finalRel))
		updatedSrc := applyTextEdits(fileTexts[origRel], editsByFile[origRel])
		info, err := os.Stat(finalAbs)
		if err != nil {
			return err
		}
		if err := os.WriteFile(finalAbs, []byte(updatedSrc), info.Mode().Perm()); err != nil {
			return err
		}
	}
	return nil
}

func twoPhaseRenameDirs(parentAbs string, ms []dirMove) error {
	type staged struct {
		tmp string
		dst string
	}
	var stage []staged
	for i, dm := range ms {
		srcAbs := filepath.Join(parentAbs, path.Base(dm.origRel))
		dstAbs := filepath.Join(parentAbs, path.Base(dm.effRel))
		tmpAbs := uniqueTempPath(parentAbs, fmt.Sprintf(".ergo-dir-rename-%d-", i))
		if err := os.Rename(srcAbs, tmpAbs); err != nil {
			return err
		}
		stage = append(stage, staged{tmp: tmpAbs, dst: dstAbs})
	}
	for _, st := range stage {
		if err := os.Rename(st.tmp, st.dst); err != nil {
			return err
		}
	}
	return nil
}

func twoPhaseRenameFiles(dirAbs string, ms []fileMove) error {
	type staged struct {
		tmp string
		dst string
	}
	var stage []staged
	for i, fm := range ms {
		srcAbs := filepath.Join(dirAbs, fm.origBase)
		dstAbs := filepath.Join(dirAbs, fm.wantBase)
		tmpAbs := uniqueTempPath(dirAbs, fmt.Sprintf(".ergo-file-rename-%d-", i))
		if err := os.Rename(srcAbs, tmpAbs); err != nil {
			return err
		}
		stage = append(stage, staged{tmp: tmpAbs, dst: dstAbs})
	}
	for _, st := range stage {
		if err := os.Rename(st.tmp, st.dst); err != nil {
			return err
		}
	}
	return nil
}

func uniqueTempPath(dirAbs, prefix string) string {
	for n := 0; ; n++ {
		cand := filepath.Join(dirAbs, fmt.Sprintf("%s%d.tmp", prefix, n))
		if _, err := os.Lstat(cand); errors.Is(err, fs.ErrNotExist) {
			return cand
		}
	}
}

func applyTextEdits(src string, edits []textEdit) string {
	if len(edits) == 0 {
		return src
	}
	sorted := slices.Clone(edits)
	slices.SortFunc(sorted, func(a, b textEdit) int {
		if a.start != b.start {
			return cmp.Compare(a.start, b.start)
		}
		return cmp.Compare(a.end, b.end)
	})
	out := src
	last := len(out) + 1
	for i := len(sorted) - 1; i >= 0; i-- {
		e := sorted[i]
		if e.end > last || e.start < 0 || e.end > len(out) {
			continue
		}
		out = out[:e.start] + e.text + out[e.end:]
		last = e.start
	}
	return out
}
