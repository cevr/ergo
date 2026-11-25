package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"ergo.local/prototype/internal/cases"
	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/lawcache"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/scripts/mutants/anchor"
)

type mutantStatus string

const (
	mutantKilled   mutantStatus = "killed"
	mutantSurvived mutantStatus = "survived"
	mutantInvalid  mutantStatus = "invalid"
	mutantUnjudged mutantStatus = "unjudged"
	mutantGap      mutantStatus = "gap"
	mutantSkipped  mutantStatus = "skipped"
)

type mutantLedger struct {
	About   string       `json:"about"`
	Env     []string     `json:"env,omitempty"`
	Mutants []*mutantRow `json:"mutants"`
}

type mutantRow struct {
	Name       string          `json:"name"`
	Origin     string          `json:"origin,omitempty"`
	File       string          `json:"file"`
	Decl       string          `json:"decl,omitempty"`
	From       string          `json:"from"`
	To         string          `json:"to"`
	Rule       string          `json:"rule,omitempty"`
	Killers    []string        `json:"killers"`
	Gap        string          `json:"gap,omitempty"`
	Anchor     *mutantAnchor   `json:"anchor,omitempty"`
	Refused    string          `json:"refused,omitempty"`
	Regenerate json.RawMessage `json:"regenerate,omitempty"`
}

type mutantAnchor struct {
	File  string   `json:"file,omitempty"`
	Text  string   `json:"text,omitempty"`
	Tests []string `json:"tests,omitempty"`
}

type mutantVerdict struct {
	Name       string       `json:"name"`
	File       string       `json:"file"`
	Decl       string       `json:"decl,omitempty"`
	Status     mutantStatus `json:"status"`
	Killer     string       `json:"killer,omitempty"`
	Runner     string       `json:"runner,omitempty"`
	Cached     bool         `json:"cached,omitempty"`
	Detail     string       `json:"detail,omitempty"`
	ReachedBy  []string     `json:"reachedBy,omitempty"`
	External   []string     `json:"externalKillers,omitempty"`
	DurationMs int64        `json:"durationMs"`
}

type mutantReport struct {
	Ledgers  []string        `json:"ledgers"`
	Killed   int             `json:"killed"`
	Cached   int             `json:"cached"`
	Survived int             `json:"survived"`
	Invalid  int             `json:"invalid"`
	Unjudged int             `json:"unjudged"`
	Gap      int             `json:"gap"`
	Skipped  int             `json:"skipped"`
	Results  []mutantVerdict `json:"results"`
}

type mutantObligation struct {
	id        string
	pkgDir    string
	pkgPath   string
	pkgRel    string
	kind      string // "test" or "law"
	name      string // test Name or law Title
	evalName  string // name matched by eval.Result.Name
	label     string // lower.TestLabel or RunTarget.Label
	digest    string
	reach     []lower.ReachedDecl
	reachKeys map[string]bool
	reachPos  map[string][]int // absFile -> sorted 1-based decl start lines
}

type mutantCacheEntry struct {
	Killer        string   `json:"killer"`
	KillerKind    string   `json:"killerKind"` // "obligation" or "example"
	KillerPkg     string   `json:"killerPkg"`
	KillerLabel   string   `json:"killerLabel"`
	Runner        string   `json:"runner"`
	BaseDigest    string   `json:"baseDigest"`
	MutatedDigest string   `json:"mutatedDigest"`
	DeclFP        string   `json:"declFP"`
	Mode          string   `json:"mode"`
	ExtraDecls    []string `json:"extraDecls,omitempty"`
}

type ledgerJob struct {
	path       string
	filterDir  string
	filterFile string
	repoRoot   string
	modRoot    string
	ledger     *mutantLedger
}

type formatState struct {
	formatted bool
}

type checkState struct {
	analysis sketchfront.Analysis
	codes    map[string]int
}

type baselineOutcome struct {
	passed   bool
	timedOut bool
	detail   string
	duration time.Duration
}

type exampleCaseTarget struct {
	name     string
	dir      string
	mainDir  string
	casesDir string
	digest   string
}

type mutantRunner struct {
	opts     fileOptions
	seed     uint64
	states   int
	platform gobind.Platform
	store    *lawcache.Store
	tcReady  bool
	tc       build.Toolchain
	tcErr    error
	tcID     string
	memLimit uint64
	timeout  time.Duration
	stderr   io.Writer

	repoRoots map[string]string
	pkgLoaded map[string]*loadedPkgInfo
	obsByID   map[string]*mutantObligation
	obsByPkg  map[string][]*mutantObligation
	allObs    []*mutantObligation
	examples  map[string]*exampleCaseTarget

	fmtCache    map[string]formatState
	chkCache    map[string]checkState
	baseCache   map[string]baselineOutcome
	baseBinDirs map[string]string // pkgDir -> temp dir holding baseline .test binary
	baseBins    map[string]string // pkgDir -> baseline .test binary path
}

func (r *mutantRunner) close() {
	for _, d := range r.baseBinDirs {
		_ = os.RemoveAll(d)
	}
}

type rowSession struct {
	analyses map[string]sketchfront.Analysis
	errs     map[string]error
	binDirs  map[string]string
	bins     map[string]string
}

func newRowSession() *rowSession {
	return &rowSession{
		analyses: map[string]sketchfront.Analysis{},
		errs:     map[string]error{},
		binDirs:  map[string]string{},
		bins:     map[string]string{},
	}
}

func (s *rowSession) close() {
	for _, d := range s.binDirs {
		_ = os.RemoveAll(d)
	}
}

func (s *rowSession) checkPkg(overlay sketchfront.FS, pkgDir string, platform gobind.Platform) (sketchfront.Analysis, error) {
	if a, ok := s.analyses[pkgDir]; ok {
		return a, s.errs[pkgDir]
	}
	a, err := sketchfront.CheckDirFS(overlay, pkgDir, platform)
	s.analyses[pkgDir] = a
	s.errs[pkgDir] = err
	return a, err
}

type loadedPkgInfo struct {
	dir      string
	rel      string
	path     string
	modRoot  string
	analysis sketchfront.Analysis
	obs      []*mutantObligation
}

func mutantsTestCommand(opts fileOptions, stdout, stderr io.Writer) error {
	var nameRe *regexp.Regexp
	if opts.run != "" {
		var err error
		if nameRe, err = regexp.Compile(opts.run); err != nil {
			return invalidInvocation(fmt.Errorf("ergo test --mutants: --run: %w", err))
		}
	}
	targets := append([]string{opts.file}, opts.more...)
	jobs, err := discoverMutantLedgers(targets)
	if err != nil {
		return err
	}
	if len(jobs) == 0 {
		return fmt.Errorf("ergo test --mutants: no testdata/mutants.json ledger found for %s", strings.Join(targets, " "))
	}
	runner, err := newMutantRunner(opts, stderr)
	if err != nil {
		return err
	}
	defer runner.close()
	defer boundLaws(runner.store)
	rep := mutantReport{Results: []mutantVerdict{}}
	for _, j := range jobs {
		rep.Ledgers = append(rep.Ledgers, j.path)
		runner.indexPackagesForJob(j)
	}
	for _, j := range jobs {
		for _, row := range j.ledger.Mutants {
			if nameRe != nil && !nameRe.MatchString(row.Name) {
				continue
			}
			absFile, baseRoot, resolveErr := resolveMutantFile(j, row.File)
			if j.filterFile != "" && filepath.Clean(absFile) != filepath.Clean(j.filterFile) {
				continue
			}
			if j.filterDir != "" && !pathWithin(absFile, j.filterDir) {
				continue
			}
			v := runner.judgeRow(j, row, absFile, baseRoot, resolveErr)
			rep.Results = append(rep.Results, v)
			switch v.Status {
			case mutantKilled:
				rep.Killed++
				if v.Cached {
					rep.Cached++
				}
			case mutantSurvived:
				rep.Survived++
			case mutantInvalid:
				rep.Invalid++
			case mutantUnjudged:
				rep.Unjudged++
			case mutantGap:
				rep.Gap++
			case mutantSkipped:
				rep.Skipped++
			}
		}
	}
	if opts.json {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			return err
		}
	} else {
		writeMutantReport(stdout, rep)
	}
	if rep.Survived > 0 || rep.Invalid > 0 || rep.Unjudged > 0 {
		return exitStatusError{code: 1, handled: true}
	}
	return nil
}

func newMutantRunner(opts fileOptions, stderr io.Writer) (*mutantRunner, error) {
	seed := uint64(1)
	if opts.seed != "" {
		var err error
		seed, err = strconv.ParseUint(opts.seed, 10, 64)
		if err != nil {
			return nil, invalidInvocation(err)
		}
	}
	states := 0
	if opts.states != "" {
		var err error
		states, err = strconv.Atoi(opts.states)
		if err != nil {
			return nil, invalidInvocation(err)
		}
	}
	memLimit, err := proclimit.Limit(os.Environ(), proclimit.DefaultLimit)
	if err != nil {
		return nil, err
	}
	timeout := 45 * time.Second
	if raw := os.Getenv("ERGO_MUTANT_TIMEOUT"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			timeout = d
		}
	}
	return &mutantRunner{
		opts:        opts,
		seed:        seed,
		states:      states,
		platform:    opts.platform,
		store:       lawStore(),
		memLimit:    memLimit,
		timeout:     timeout,
		stderr:      stderr,
		repoRoots:   map[string]string{},
		pkgLoaded:   map[string]*loadedPkgInfo{},
		obsByID:     map[string]*mutantObligation{},
		obsByPkg:    map[string][]*mutantObligation{},
		examples:    map[string]*exampleCaseTarget{},
		fmtCache:    map[string]formatState{},
		chkCache:    map[string]checkState{},
		baseCache:   map[string]baselineOutcome{},
		baseBinDirs: map[string]string{},
		baseBins:    map[string]string{},
	}, nil
}

func (r *mutantRunner) toolchain() (build.Toolchain, string, error) {
	if !r.tcReady {
		r.tcReady = true
		r.tc, r.tcErr = buildAction(r.opts)
		if r.tcErr == nil {
			r.tcID = nativeToolchain(r.opts, r.tc)
		}
	}
	return r.tc, r.tcID, r.tcErr
}

func (r *mutantRunner) modeTag(runner string) string {
	if r.opts.native || runner == "native" || runner == "cases" {
		_, id, err := r.toolchain()
		if err == nil && id != "" {
			return "native:" + id
		}
		return "native"
	}
	return "eval"
}

func discoverMutantLedgers(targets []string) ([]ledgerJob, error) {
	seen := map[string]bool{}
	var jobs []ledgerJob
	addLedger := func(ledgerPath, filterDir, filterFile string) error {
		clean := filepath.Clean(ledgerPath)
		key := clean + "\x00" + filterDir + "\x00" + filterFile
		if seen[key] {
			return nil
		}
		seen[key] = true
		l, err := readMutantLedger(clean)
		if err != nil {
			return err
		}
		parent := filepath.Dir(filepath.Dir(clean))
		repoRoot := findRepoRoot(parent)
		modRoot, _ := moduleRoot(parent)
		jobs = append(jobs, ledgerJob{
			path:       clean,
			filterDir:  filterDir,
			filterFile: filterFile,
			repoRoot:   repoRoot,
			modRoot:    modRoot,
			ledger:     l,
		})
		return nil
	}
	for _, raw := range targets {
		if recursivePattern(raw) {
			base := filepath.Dir(filepath.Clean(raw))
			abs, err := absClean(base)
			if err != nil {
				return nil, err
			}
			var found []string
			err = filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					name := d.Name()
					if p != abs && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "dist" || name == "vendor") {
						return filepath.SkipDir
					}
					return nil
				}
				if d.Name() == "mutants.json" && filepath.Base(filepath.Dir(p)) == "testdata" {
					found = append(found, p)
				}
				return nil
			})
			if err != nil {
				return nil, err
			}
			sort.Strings(found)
			for _, lp := range found {
				if err := addLedger(lp, "", ""); err != nil {
					return nil, err
				}
			}
			continue
		}
		abs, err := absClean(raw)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(abs)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			dir := filepath.Dir(abs)
			lp := findEnclosingLedger(dir)
			if lp == "" {
				continue
			}
			if err := addLedger(lp, "", abs); err != nil {
				return nil, err
			}
			continue
		}
		direct := filepath.Join(abs, "testdata", "mutants.json")
		if isRegularFile(direct) {
			if err := addLedger(direct, "", ""); err != nil {
				return nil, err
			}
			continue
		}
		if lp := findEnclosingLedger(abs); lp != "" {
			if err := addLedger(lp, abs, ""); err != nil {
				return nil, err
			}
		}
	}
	return jobs, nil
}

func findEnclosingLedger(dir string) string {
	mod, hasMod := moduleRoot(dir)
	for d := dir; ; d = filepath.Dir(d) {
		p := filepath.Join(d, "testdata", "mutants.json")
		if isRegularFile(p) {
			return p
		}
		if hasMod && d == mod {
			break
		}
		if filepath.Dir(d) == d {
			break
		}
	}
	return ""
}

func readMutantLedger(path string) (*mutantLedger, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var l mutantLedger
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &l, nil
}

func findRepoRoot(dir string) string {
	for d := dir; ; d = filepath.Dir(d) {
		if isRegularFile(filepath.Join(d, "go.mod")) {
			// Prefer the repository root (holding .git) when nested go.mod exists.
			if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
				return d
			}
		}
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		if filepath.Dir(d) == d {
			break
		}
	}
	for d := dir; ; d = filepath.Dir(d) {
		if isRegularFile(filepath.Join(d, "go.mod")) {
			return d
		}
		if filepath.Dir(d) == d {
			return ""
		}
	}
}

func resolveMutantFile(j ledgerJob, rel string) (absFile, baseRoot string, err error) {
	slash := filepath.FromSlash(rel)
	candidates := []string{}
	if j.repoRoot != "" {
		candidates = append(candidates, j.repoRoot)
	}
	if j.modRoot != "" && j.modRoot != j.repoRoot {
		candidates = append(candidates, j.modRoot)
	}
	parent := filepath.Dir(filepath.Dir(j.path))
	if !slices.Contains(candidates, parent) {
		candidates = append(candidates, parent)
	}
	for _, root := range candidates {
		p := filepath.Join(root, slash)
		if isRegularFile(p) {
			return filepath.Clean(p), root, nil
		}
	}
	if len(candidates) > 0 {
		return filepath.Clean(filepath.Join(candidates[0], slash)), candidates[0], fmt.Errorf("file %s not found", rel)
	}
	return filepath.Clean(slash), "", fmt.Errorf("file %s not found", rel)
}

func (r *mutantRunner) indexPackagesForJob(j ledgerJob) {
	ledgerParent := filepath.Dir(filepath.Dir(j.path))
	root := j.repoRoot
	if root == "" {
		root = j.modRoot
	}
	if root == "" {
		root = ledgerParent
	}
	r.discoverAndIndexDir(ledgerParent, root)
	// Also index any explicit Ergo package named in row killers or anchor tests.
	for _, row := range j.ledger.Mutants {
		if !strings.HasSuffix(row.File, ".ergo") {
			continue
		}
		if abs, _, err := resolveMutantFile(j, row.File); err == nil {
			r.loadPkgDir(filepath.Dir(abs), root)
		}
		for _, k := range allKillerIDs(row) {
			suite, _ := splitKillerID(k)
			if suite == "" {
				continue
			}
			if suite == "examples" {
				_, rest := splitKillerID(k)
				if mod, ok := strings.CutPrefix(rest, "modules/"); ok {
					dir := filepath.Join(root, "modules", filepath.FromSlash(mod))
					r.discoverAndIndexDir(dir, root)
				}
				continue
			}
			cand := filepath.Join(root, filepath.FromSlash(suite))
			if dirHasErgo(cand) {
				r.loadPkgDir(cand, root)
			}
		}
	}
}

func (r *mutantRunner) discoverAndIndexDir(dir, repoRoot string) {
	abs, err := absClean(dir)
	if err != nil {
		return
	}
	pkgs, err := sketchfront.DiscoverPackagesThrough(nil, abs)
	if err == nil && len(pkgs) > 0 {
		for _, p := range pkgs {
			r.loadPkgDir(p.Dir, repoRoot)
		}
		return
	}
	if dirHasErgo(abs) {
		r.loadPkgDir(abs, repoRoot)
	}
}

func (r *mutantRunner) loadPkgDir(dir, repoRoot string) *loadedPkgInfo {
	abs, err := absClean(dir)
	if err != nil {
		return nil
	}
	if info, ok := r.pkgLoaded[abs]; ok {
		return info
	}
	if !dirHasErgo(abs) {
		r.pkgLoaded[abs] = nil
		return nil
	}
	analysis, err := sketchfront.CheckDirForPlatform(abs, r.platform)
	if err != nil || len(analysis.Diagnostics) > 0 || analysis.Root().Package == nil {
		r.pkgLoaded[abs] = nil
		return nil
	}
	pkg := analysis.Root().Package
	rel := abs
	if repoRoot != "" {
		if s, err := filepath.Rel(repoRoot, abs); err == nil && !strings.HasPrefix(s, "..") {
			rel = filepath.ToSlash(s)
		}
	}
	modRoot, _ := moduleRoot(abs)
	info := &loadedPkgInfo{
		dir:      abs,
		rel:      rel,
		path:     analysis.Root().Path,
		modRoot:  modRoot,
		analysis: analysis,
	}
	pkgDirs := map[*check.Package]string{pkg: abs}
	dirMap := map[string]string{info.path: abs}
	for _, p := range analysis.Packages() {
		dirMap[p.Path] = p.Dir
		if p.Package != nil {
			pkgDirs[p.Package] = p.Dir
		}
	}
	for _, t := range pkg.Tests {
		reach := lower.TestReach(pkg, t)
		ob := buildObligation(abs, info.path, rel, "test", t.Name, t.Name, lower.TestLabel(t), lower.TestDigest(pkg, t), reach, pkgDirs, dirMap)
		info.obs = append(info.obs, ob)
		r.obsByID[ob.id] = ob
		r.allObs = append(r.allObs, ob)
	}
	for _, rt := range lower.RunTargets(pkg) {
		reach := rt.Reach(pkg)
		ob := buildObligation(abs, info.path, rel, "law", rt.Law.Title(pkg), rt.Label, rt.Label, rt.Digest(pkg), reach, pkgDirs, dirMap)
		info.obs = append(info.obs, ob)
		r.obsByID[ob.id] = ob
		r.allObs = append(r.allObs, ob)
	}
	r.pkgLoaded[abs] = info
	r.obsByPkg[abs] = info.obs
	r.obsByPkg[rel] = info.obs
	return info
}

func buildObligation(pkgDir, pkgPath, pkgRel, kind, name, evalName, label, digest string, reach []lower.ReachedDecl, pkgDirs map[*check.Package]string, dirMap map[string]string) *mutantObligation {
	keys := make(map[string]bool, len(reach))
	pos := map[string][]int{}
	for _, d := range reach {
		keys[d.Key] = true
		f, line := d.Position()
		if f == "" || line <= 0 {
			continue
		}
		absF := f
		if !filepath.IsAbs(absF) {
			dir := ""
			if d.Pkg != nil {
				dir = pkgDirs[d.Pkg]
				if dir == "" {
					dir = dirMap[d.Pkg.Path]
				}
			}
			if dir != "" {
				// In Ergo every source file of a package lives directly in its
				// package directory, while f may be module-root-relative.
				absF = filepath.Join(dir, filepath.Base(f))
			}
		}
		absF = filepath.Clean(absF)
		pos[absF] = append(pos[absF], line)
	}
	for f := range pos {
		slices.Sort(pos[f])
	}
	id := pkgRel + ":" + name
	if kind == "law" {
		id = pkgRel + ":law:" + name
		if label != "law "+name {
			id = pkgRel + ":" + label
		}
	}
	return &mutantObligation{
		id:        id,
		pkgDir:    pkgDir,
		pkgPath:   pkgPath,
		pkgRel:    pkgRel,
		kind:      kind,
		name:      name,
		evalName:  evalName,
		label:     label,
		digest:    digest,
		reach:     reach,
		reachKeys: keys,
		reachPos:  pos,
	}
}

func (r *mutantRunner) judgeRow(j ledgerJob, row *mutantRow, absFile, baseRoot string, resolveErr error) mutantVerdict {
	start := time.Now()
	v := mutantVerdict{Name: row.Name, File: row.File, Decl: row.Decl}
	defer func() {
		v.DurationMs = time.Since(start).Milliseconds()
	}()
	if !strings.HasSuffix(row.File, ".ergo") {
		v.Status = mutantSkipped
		v.Detail = "non-.ergo file (handled by scripts/mutants)"
		return v
	}
	if resolveErr != nil {
		v.Status = mutantInvalid
		v.Detail = resolveErr.Error()
		return v
	}
	original, err := os.ReadFile(absFile)
	if err != nil {
		v.Status = mutantInvalid
		v.Detail = err.Error()
		return v
	}
	at, err := anchor.Locate(row.File, original, row.Decl, row.From)
	if err != nil {
		v.Status = mutantInvalid
		v.Detail = err.Error()
		return v
	}
	mutated, err := anchor.Apply(row.File, original, row.Decl, row.From, row.To)
	if err != nil {
		v.Status = mutantInvalid
		v.Detail = err.Error()
		return v
	}
	declName, declSpan := enclosingDeclInfo(row.File, original, row.Decl, at, len(row.From))
	if v.Decl == "" && declName != "" {
		v.Decl = declName
	}
	declFP := declFingerprint(original, declSpan)
	mutPkgDir := filepath.Dir(absFile)
	mutPkgInfo := r.loadPkgDir(mutPkgDir, baseRoot)
	mutPkgPath := mutPkgDir
	if mutPkgInfo != nil {
		mutPkgPath = mutPkgInfo.path
	}

	// Build in-memory overlay: ZERO disk mutations.
	overlay := sketchfront.OverlayFS(map[string]string{filepath.Clean(absFile): string(mutated)})
	sess := newRowSession()
	defer sess.close()

	// Collect candidate killers (named first, then graph read-set reach).
	namedObs, exampleTargets, external := r.resolveNamedKillers(j, row, baseRoot, absFile, declSpan)
	v.External = external
	reachObs := r.reachingObligations(absFile, declSpan, mutPkgDir)
	candidates := mergeObligations(namedObs, reachObs)

	// Reach-digest verdict cache lookup (only when not --fresh).
	modePref := r.modeTag("")
	rowKey := mutantRowKey(mutPkgPath, row.File, row.Decl, declFP, row.From, row.To, row.Refused, modePref, r.seed, r.states)
	if !r.opts.fresh {
		if cached, ok := r.lookupCache(rowKey, declFP, row, overlay, sess, candidates, exampleTargets); ok {
			v.Status = mutantKilled
			v.Killer = cached.Killer
			v.Runner = "cached"
			v.Cached = true
			return v
		}
	}

	// In-process validity check (FK7a formatter + FK12a checker diagnostics).
	if row.Refused == "" {
		if why := r.validateMutant(absFile, original, mutated, overlay, sess); why != "" {
			v.Status = mutantInvalid
			v.Detail = why
			return v
		}
	}

	var reachedPassed []string
	var baselineReds []string
	timedOut := false
	ranAny := false

	for _, ob := range candidates {
		base := r.checkBaseline(ob)
		if !base.passed {
			baselineReds = append(baselineReds, ob.id+" ("+base.detail+")")
			continue
		}
		ranAny = true
		outcome, runnerKind, mutDigest, extraDecls := r.runObligationOnMutant(ob, overlay, sess, base.duration, row.Refused != "")
		switch outcome {
		case "killed":
			v.Status = mutantKilled
			v.Killer = ob.id
			v.Runner = runnerKind
			r.storeCache(rowKey, mutantCacheEntry{
				Killer:        ob.id,
				KillerKind:    "obligation",
				KillerPkg:     ob.pkgDir,
				KillerLabel:   ob.label,
				Runner:        runnerKind,
				BaseDigest:    ob.digest,
				MutatedDigest: mutDigest,
				DeclFP:        declFP,
				Mode:          r.modeTag(runnerKind),
				ExtraDecls:    extraDecls,
			}, row)
			return v
		case "timeout":
			timedOut = true
		case "passed":
			reachedPassed = append(reachedPassed, ob.id)
		}
	}

	// Try named example case targets (e.g. examples:work) via in-memory overlay.
	for _, ex := range exampleTargets {
		base := r.checkExampleBaseline(ex)
		if !base.passed {
			baselineReds = append(baselineReds, "examples:"+ex.name+" ("+base.detail+")")
			continue
		}
		ranAny = true
		outcome, mutDigest := r.runExampleOnMutant(ex, overlay, base.duration)
		switch outcome {
		case "killed":
			v.Status = mutantKilled
			v.Killer = "examples:" + ex.name
			v.Runner = "cases"
			r.storeCache(rowKey, mutantCacheEntry{
				Killer:        "examples:" + ex.name,
				KillerKind:    "example",
				KillerPkg:     ex.mainDir,
				KillerLabel:   "examples:" + ex.name,
				Runner:        "cases",
				BaseDigest:    ex.digest,
				MutatedDigest: mutDigest,
				DeclFP:        declFP,
				Mode:          r.modeTag("cases"),
			}, row)
			return v
		case "timeout":
			timedOut = true
		case "passed":
			reachedPassed = append(reachedPassed, "examples:"+ex.name)
		}
	}

	v.ReachedBy = reachedPassed
	switch {
	case timedOut:
		v.Status = mutantUnjudged
		v.Detail = "killer timed out (timeouts never count as kills)"
	case !ranAny && len(baselineReds) > 0:
		v.Status = mutantUnjudged
		v.Detail = "baseline failed: " + strings.Join(baselineReds, "; ")
	case row.Gap != "":
		v.Status = mutantGap
		v.Detail = row.Gap
	case len(baselineReds) > 0 && len(reachedPassed) == 0:
		v.Status = mutantUnjudged
		v.Detail = "baseline failed: " + strings.Join(baselineReds, "; ")
	default:
		v.Status = mutantSurvived
		if len(reachedPassed) == 0 && len(external) > 0 {
			v.Detail = "no native Ergo killer reached/killed row (external killers: " + strings.Join(external, ", ") + ")"
		}
	}
	return v
}

func (r *mutantRunner) validateMutant(absFile string, original, mutated []byte, overlay sketchfront.FS, sess *rowSession) string {
	// Format check (FK7a).
	beforeFmt, ok := r.fmtCache[absFile]
	if !ok {
		res, err := sketchfront.Format(string(original), 0)
		beforeFmt = formatState{formatted: err == nil && !res.Changed}
		r.fmtCache[absFile] = beforeFmt
	}
	var reasons []string
	if beforeFmt.formatted {
		res, err := sketchfront.Format(string(mutated), 0)
		if err != nil || res.Changed {
			reasons = append(reasons, "the mutated source is not written as the formatter prints it (FK7a)")
		}
	}
	// Check diagnostics (FK12a).
	pkgDir := filepath.Dir(absFile)
	beforeChk, ok := r.chkCache[pkgDir]
	if !ok {
		a, err := sketchfront.CheckDirForPlatform(pkgDir, r.platform)
		codes := map[string]int{}
		if err != nil {
			codes["load-error"]++
		} else {
			for _, d := range a.Diagnostics {
				codes[d.Code]++
			}
		}
		beforeChk = checkState{analysis: a, codes: codes}
		r.chkCache[pkgDir] = beforeChk
	}
	afterA, err := sess.checkPkg(overlay, pkgDir, r.platform)
	afterCodes := map[string]int{}
	if err != nil {
		afterCodes["load-error"]++
	} else {
		for _, d := range afterA.Diagnostics {
			afterCodes[d.Code]++
		}
	}
	var newCodes []string
	for code, count := range afterCodes {
		if count > beforeChk.codes[code] {
			newCodes = append(newCodes, code)
		}
	}
	sort.Strings(newCodes)
	if len(newCodes) > 0 {
		reasons = append(reasons, "the compiler refuses the mutated source (FK12a): new "+strings.Join(newCodes, ", "))
	}
	return strings.Join(reasons, "; ")
}

func (r *mutantRunner) resolveNamedKillers(j ledgerJob, row *mutantRow, baseRoot, absFile string, declSpan declLineSpan) (obs []*mutantObligation, examples []*exampleCaseTarget, external []string) {
	seenObs := map[string]bool{}
	seenEx := map[string]bool{}
	seenExt := map[string]bool{}
	addOb := func(ob *mutantObligation) {
		if ob != nil && !seenObs[ob.id] {
			seenObs[ob.id] = true
			obs = append(obs, ob)
		}
	}
	for _, raw := range allKillerIDs(row) {
		suite, name := splitKillerID(raw)
		if suite == "" || name == "" {
			continue
		}
		if suite == "examples" {
			if mod, ok := strings.CutPrefix(name, "modules/"); ok {
				modDir := filepath.Join(baseRoot, "modules", filepath.FromSlash(mod))
				for _, ob := range r.reachingObligationsInRoot(absFile, declSpan, modDir) {
					addOb(ob)
				}
				continue
			}
			if ex := r.loadExampleTarget(baseRoot, name); ex != nil {
				if !seenEx[ex.name] {
					seenEx[ex.name] = true
					examples = append(examples, ex)
				}
				continue
			}
			if !seenExt[raw] {
				seenExt[raw] = true
				external = append(external, raw)
			}
			continue
		}
		pkgDir := filepath.Join(baseRoot, filepath.FromSlash(suite))
		info := r.loadPkgDir(pkgDir, baseRoot)
		if info == nil {
			if !seenExt[raw] {
				seenExt[raw] = true
				external = append(external, raw)
			}
			continue
		}
		matched := false
		if lawName, isLaw := strings.CutPrefix(name, "law:"); isLaw {
			for _, ob := range info.obs {
				if ob.kind == "law" && lawNameMatches(ob.name, ob.label, lawName) {
					addOb(ob)
					matched = true
				}
			}
		} else {
			for _, ob := range info.obs {
				if ob.kind == "test" && ob.name == name {
					addOb(ob)
					matched = true
				}
			}
		}
		if !matched && !seenExt[raw] {
			seenExt[raw] = true
			external = append(external, raw)
		}
	}
	return obs, examples, external
}

func lawNameMatches(title, label, want string) bool {
	if title == want || label == "law "+want {
		return true
	}
	// Handle qualified member law names (e.g. board.Store.StalePosition vs Store.StalePosition).
	if strings.HasSuffix(title, "."+want) || strings.HasSuffix(want, "."+title) {
		return true
	}
	return false
}

func (r *mutantRunner) reachingObligations(absFile string, declSpan declLineSpan, mutPkgDir string) []*mutantObligation {
	var samePkg, otherPkg []*mutantObligation
	for _, ob := range r.allObs {
		if !obligationReaches(ob, absFile, declSpan) {
			continue
		}
		if ob.pkgDir == mutPkgDir {
			samePkg = append(samePkg, ob)
		} else {
			otherPkg = append(otherPkg, ob)
		}
	}
	sortObligations(samePkg)
	sortObligations(otherPkg)
	return append(samePkg, otherPkg...)
}

func (r *mutantRunner) reachingObligationsInRoot(absFile string, declSpan declLineSpan, rootDir string) []*mutantObligation {
	absRoot, err := absClean(rootDir)
	if err != nil {
		return nil
	}
	var samePkg, otherPkg []*mutantObligation
	mutPkgDir := filepath.Dir(absFile)
	for _, ob := range r.allObs {
		if !pathWithin(ob.pkgDir, absRoot) {
			continue
		}
		if !obligationReaches(ob, absFile, declSpan) {
			continue
		}
		if ob.pkgDir == mutPkgDir {
			samePkg = append(samePkg, ob)
		} else {
			otherPkg = append(otherPkg, ob)
		}
	}
	sortObligations(samePkg)
	sortObligations(otherPkg)
	return append(samePkg, otherPkg...)
}

func sortObligations(obs []*mutantObligation) {
	sort.Slice(obs, func(i, j int) bool {
		if obs[i].kind != obs[j].kind {
			// Tests (examples) tend to be cheaper than sampled laws.
			return obs[i].kind == "test"
		}
		return obs[i].id < obs[j].id
	})
}

func mergeObligations(first, second []*mutantObligation) []*mutantObligation {
	seen := map[string]bool{}
	out := make([]*mutantObligation, 0, len(first)+len(second))
	for _, list := range [][]*mutantObligation{first, second} {
		for _, ob := range list {
			if !seen[ob.id] {
				seen[ob.id] = true
				out = append(out, ob)
			}
		}
	}
	return out
}

func obligationReaches(ob *mutantObligation, absFile string, span declLineSpan) bool {
	lines, ok := ob.reachPos[filepath.Clean(absFile)]
	if !ok {
		return false
	}
	if span.startLine == 0 {
		return true
	}
	for _, l := range lines {
		if l >= span.startLine && l <= span.endLine {
			return true
		}
	}
	if span.name != "" {
		suffix := "." + span.name
		for k := range ob.reachKeys {
			if strings.HasSuffix(k, suffix) {
				return true
			}
		}
	}
	return false
}

func (r *mutantRunner) checkBaseline(ob *mutantObligation) baselineOutcome {
	if got, ok := r.baseCache[ob.id]; ok {
		return got
	}
	start := time.Now()
	info := r.pkgLoaded[ob.pkgDir]
	if info == nil {
		out := baselineOutcome{passed: false, detail: "package failed to check"}
		r.baseCache[ob.id] = out
		return out
	}
	status, detail := r.runObligationAnalysis(ob, info.analysis, nil, r.timeout, true)
	out := baselineOutcome{
		passed:   status == "passed",
		timedOut: status == "timeout",
		detail:   detail,
		duration: time.Since(start),
	}
	r.baseCache[ob.id] = out
	return out
}

func (r *mutantRunner) runObligationOnMutant(ob *mutantObligation, overlay sketchfront.FS, sess *rowSession, baseDur time.Duration, allowRefusal bool) (outcome, runnerKind, mutDigest string, extraDecls []string) {
	a, err := sess.checkPkg(overlay, ob.pkgDir, r.platform)
	if err != nil {
		return "unjudged", "", "", nil
	}
	if len(a.Diagnostics) > 0 || a.Root().Package == nil {
		// Dependent package failed to type-check against the mutant (e.g. signature break or refused row).
		if allowRefusal || ob.pkgDir != "" {
			return "killed", "check", "", nil
		}
		return "unjudged", "", "", nil
	}
	mutDigest, extraDecls = mutatedObligationDigest(a, ob)
	timeout := r.timeout
	if baseDur > 0 && 10*baseDur > timeout {
		timeout = 10 * baseDur
	}
	status, kind := r.runObligationAnalysisWithRunner(ob, a, sess, timeout, false)
	return status, kind, mutDigest, extraDecls
}

func (r *mutantRunner) runObligationAnalysis(ob *mutantObligation, a sketchfront.Analysis, sess *rowSession, timeout time.Duration, isBaseline bool) (status, detail string) {
	st, kind := r.runObligationAnalysisWithRunner(ob, a, sess, timeout, isBaseline)
	if st == "passed" {
		return "passed", kind
	}
	return st, kind
}

func (r *mutantRunner) runObligationAnalysisWithRunner(ob *mutantObligation, a sketchfront.Analysis, sess *rowSession, timeout time.Duration, isBaseline bool) (status, runnerOrDetail string) {
	if !r.opts.native {
		b := eval.DefaultBudget()
		b.Seed = r.seed
		b.States = r.states
		b.Source = ob.pkgDir
		b.Select = func(name string) bool { return name == ob.evalName }
		rep, err := a.Evaluate(b)
		if err == nil {
			for _, res := range rep.Results {
				if res.Name != ob.evalName {
					continue
				}
				switch res.Status {
				case eval.Passed:
					return "passed", "eval"
				case eval.Refuted, eval.Defect:
					if isBaseline {
						return "failed", string(res.Status) + ": " + res.Reason
					}
					return "killed", "eval"
				case eval.Skipped, eval.Incomplete, eval.Native:
					// Undecided at check time: fall through to native binary.
				}
			}
		}
	}
	return r.runObligationNative(ob, a, sess, timeout, isBaseline)
}

func (r *mutantRunner) buildNativeTestBinary(pkgDir string, a sketchfront.Analysis, sess *rowSession) (string, error) {
	if sess == nil {
		if bin, ok := r.baseBins[pkgDir]; ok {
			return bin, nil
		}
	} else {
		if bin, ok := sess.bins[pkgDir]; ok {
			return bin, nil
		}
	}
	tc, _, err := r.toolchain()
	if err != nil {
		return "", err
	}
	root, err := build.DefaultRoot()
	if err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp("", "ergo-mutant-")
	if err != nil {
		return "", err
	}
	binary := filepath.Join(tmp, filepath.Base(pkgDir)+".test")
	req := sketchfront.BuildRequest{
		Mode:       build.ModeTest,
		Source:     pkgDir,
		Output:     binary,
		Cache:      build.Cache{Root: root},
		Toolchain:  tc,
		IR:         true,
		NoViews:    r.opts.noViews,
		NoFallback: fallbackOff(r.opts),
		Stderr:     io.Discard,
	}
	if _, err := sketchfront.Build(a, req); err != nil {
		_ = os.RemoveAll(tmp)
		return "", err
	}
	if sess == nil {
		r.baseBinDirs[pkgDir] = tmp
		r.baseBins[pkgDir] = binary
	} else {
		sess.binDirs[pkgDir] = tmp
		sess.bins[pkgDir] = binary
	}
	return binary, nil
}

func (r *mutantRunner) runObligationNative(ob *mutantObligation, a sketchfront.Analysis, sess *rowSession, timeout time.Duration, isBaseline bool) (status, detail string) {
	binary, err := r.buildNativeTestBinary(ob.pkgDir, a, sess)
	if err != nil {
		return "unjudged", err.Error()
	}
	childArgs := []string{"--source", ob.pkgDir, "--seed", strconv.FormatUint(r.seed, 10)}
	if r.states > 0 {
		childArgs = append(childArgs, "--states", strconv.Itoa(r.states))
	}
	if ob.kind == "law" {
		childArgs = append(childArgs, "--law", ob.name)
	} else {
		childArgs = append(childArgs, "--run", ob.name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := testProgramCommand(binary, childArgs, r.memLimit)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := proclimit.Start(cmd, r.memLimit); err != nil {
		return "unjudged", err.Error()
	}
	runErr := proclimit.Wait(ctx, cmd)
	if ctx.Err() == context.DeadlineExceeded {
		return "timeout", "timed out"
	}
	if runErr != nil {
		if isBaseline {
			return "failed", runErr.Error()
		}
		return "killed", "native"
	}
	if hasSkipLine(out.String()) {
		return "unjudged", "skipped"
	}
	return "passed", "native"
}

func hasSkipLine(out string) bool {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "skip ") {
			return true
		}
	}
	return false
}

func (r *mutantRunner) loadExampleTarget(baseRoot, name string) *exampleCaseTarget {
	if baseRoot == "" {
		return nil
	}
	if ex, ok := r.examples[name]; ok {
		return ex
	}
	exDir := filepath.Join(baseRoot, "examples", filepath.FromSlash(name))
	casesDir := filepath.Join(exDir, "cases")
	if info, err := os.Stat(casesDir); err != nil || !info.IsDir() {
		r.examples[name] = nil
		return nil
	}
	mainDir := findMainPackageDir(exDir)
	if mainDir == "" {
		r.examples[name] = nil
		return nil
	}
	digest := r.exampleDigest(mainDir, casesDir, nil)
	ex := &exampleCaseTarget{
		name:     name,
		dir:      exDir,
		mainDir:  mainDir,
		casesDir: casesDir,
		digest:   digest,
	}
	r.examples[name] = ex
	return ex
}

func findMainPackageDir(dir string) string {
	var found string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || found != "" {
			return nil
		}
		if d.IsDir() {
			n := d.Name()
			if p != dir && (strings.HasPrefix(n, ".") || n == "cases" || n == "testdata") {
				return filepath.SkipDir
			}
			if isRegularFile(filepath.Join(p, "main.ergo")) {
				found = p
			}
		}
		return nil
	})
	return found
}

func (r *mutantRunner) exampleDigest(mainDir, casesDir string, overlay sketchfront.FS) string {
	var a sketchfront.Analysis
	var err error
	if overlay == nil {
		a, err = sketchfront.CheckDirForPlatform(mainDir, r.platform)
	} else {
		a, err = sketchfront.CheckDirFS(overlay, mainDir, r.platform)
	}
	if err != nil || len(a.Diagnostics) > 0 {
		return ""
	}
	h := sha256.New()
	for _, p := range a.Packages() {
		fmt.Fprintf(h, "pkg:%s\x00", p.Path)
		if p.Package != nil {
			for _, f := range p.Package.Files() {
				fmt.Fprintf(h, "file:%s:%s\x00", f.Name, syntax.CheckText(f.Src))
			}
		}
	}
	_ = filepath.WalkDir(casesDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if data, rerr := os.ReadFile(p); rerr == nil {
			rel, _ := filepath.Rel(casesDir, p)
			fmt.Fprintf(h, "case:%s:%d:%s\x00", filepath.ToSlash(rel), len(data), hex.EncodeToString(sha256Sum(data)))
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))
}

func (r *mutantRunner) checkExampleBaseline(ex *exampleCaseTarget) baselineOutcome {
	key := "example:" + ex.name
	if got, ok := r.baseCache[key]; ok {
		return got
	}
	start := time.Now()
	st, detail := r.runExample(ex, nil, r.timeout, true)
	out := baselineOutcome{
		passed:   st == "passed",
		timedOut: st == "timeout",
		detail:   detail,
		duration: time.Since(start),
	}
	r.baseCache[key] = out
	return out
}

func (r *mutantRunner) runExampleOnMutant(ex *exampleCaseTarget, overlay sketchfront.FS, baseDur time.Duration) (string, string) {
	timeout := r.timeout
	if baseDur > 0 && 10*baseDur > timeout {
		timeout = 10 * baseDur
	}
	mutDigest := r.exampleDigest(ex.mainDir, ex.casesDir, overlay)
	st, _ := r.runExample(ex, overlay, timeout, false)
	return st, mutDigest
}

func (r *mutantRunner) runExample(ex *exampleCaseTarget, overlay sketchfront.FS, timeout time.Duration, isBaseline bool) (string, string) {
	var a sketchfront.Analysis
	var err error
	if overlay == nil {
		a, err = sketchfront.CheckDirForPlatform(ex.mainDir, r.platform)
	} else {
		a, err = sketchfront.CheckDirFS(overlay, ex.mainDir, r.platform)
	}
	if err != nil || len(a.Diagnostics) > 0 {
		if isBaseline {
			return "failed", "example check failed"
		}
		return "killed", "check"
	}
	tc, _, err := r.toolchain()
	if err != nil {
		return "unjudged", err.Error()
	}
	root, err := build.DefaultRoot()
	if err != nil {
		return "unjudged", err.Error()
	}
	tmp, err := os.MkdirTemp("", "ergo-mutant-cases-")
	if err != nil {
		return "unjudged", err.Error()
	}
	defer os.RemoveAll(tmp)
	bin := filepath.Join(tmp, ex.name)
	req := sketchfront.BuildRequest{
		Mode:       build.ModeBuild,
		Source:     ex.mainDir,
		Output:     bin,
		Cache:      build.Cache{Root: root},
		Toolchain:  tc,
		IR:         true,
		NoViews:    r.opts.noViews,
		NoFallback: fallbackOff(r.opts),
		Stderr:     io.Discard,
	}
	if _, err := sketchfront.Build(a, req); err != nil {
		return "unjudged", err.Error()
	}
	loaded, problems := cases.Load(ex.casesDir, false)
	if len(problems) > 0 {
		return "unjudged", "cases load problems"
	}
	live := cases.NewLive()
	done := make(chan int, 1)
	go func() {
		done <- live.Run(io.Discard, bin, loaded, r.memLimit)
	}()
	select {
	case failed := <-done:
		if failed == 0 {
			return "passed", "cases"
		}
		if isBaseline {
			return "failed", fmt.Sprintf("%d cases failed", failed)
		}
		return "killed", "cases"
	case <-time.After(timeout):
		return "timeout", "timed out"
	}
}

func mutatedObligationDigest(a sketchfront.Analysis, ob *mutantObligation) (digest string, extra []string) {
	pkg := a.Root().Package
	if pkg == nil {
		return "", nil
	}
	var reach []lower.ReachedDecl
	if ob.kind == "test" {
		for _, t := range pkg.Tests {
			if t.Name == ob.name {
				digest = lower.TestDigest(pkg, t)
				reach = lower.TestReach(pkg, t)
				break
			}
		}
	} else {
		for _, rt := range lower.RunTargets(pkg) {
			if rt.Label == ob.label {
				digest = rt.Digest(pkg)
				reach = rt.Reach(pkg)
				break
			}
		}
	}
	for _, d := range reach {
		if !ob.reachKeys[d.Key] {
			extra = append(extra, d.Key)
		}
	}
	sort.Strings(extra)
	return digest, extra
}

func mutantEditFingerprint(decl, from, to, refused string) string {
	return lawcache.Key("mutant-edit/1", decl, from, to, refused)
}

func mutantRowKey(pkgPath, relFile, decl, declFP, from, to, refused, mode string, seed uint64, states int) string {
	editFP := mutantEditFingerprint(decl, from, to, refused)
	return lawcache.Key(
		"mutant-row/1",
		pkgPath, relFile, declFP, editFP, mode,
		strconv.FormatUint(seed, 10), strconv.Itoa(states),
	)
}

func mutantVerdictKey(declFP, from, to, decl, refused, killerPkg, killerLabel, mutatedDigest, mode string, seed uint64, states int) string {
	editFP := mutantEditFingerprint(decl, from, to, refused)
	return lawcache.Key(
		"mutant-verdict/1",
		declFP, editFP, killerPkg, killerLabel, mutatedDigest, mode,
		strconv.FormatUint(seed, 10), strconv.Itoa(states),
	)
}

func (r *mutantRunner) lookupCache(rowKey, declFP string, row *mutantRow, overlay sketchfront.FS, sess *rowSession, candidates []*mutantObligation, examples []*exampleCaseTarget) (mutantCacheEntry, bool) {
	if r.store == nil {
		return mutantCacheEntry{}, false
	}
	raw, ok := r.store.Get(rowKey)
	if !ok {
		return mutantCacheEntry{}, false
	}
	var entry mutantCacheEntry
	if json.Unmarshal([]byte(raw), &entry) != nil || entry.DeclFP != declFP || entry.Killer == "" {
		return mutantCacheEntry{}, false
	}
	if r.opts.native && !strings.HasPrefix(entry.Mode, "native") {
		return mutantCacheEntry{}, false
	}
	if entry.KillerKind == "example" {
		for _, ex := range examples {
			if "examples:"+ex.name == entry.Killer && ex.digest != "" && ex.digest == entry.BaseDigest {
				vk := mutantVerdictKey(declFP, row.From, row.To, row.Decl, row.Refused, entry.KillerPkg, entry.KillerLabel, entry.MutatedDigest, entry.Mode, r.seed, r.states)
				if _, ok := r.store.Get(vk); ok {
					return entry, true
				}
			}
		}
		return mutantCacheEntry{}, false
	}
	var matched *mutantObligation
	for _, ob := range candidates {
		if ob.id == entry.Killer && ob.label == entry.KillerLabel {
			matched = ob
			break
		}
	}
	if matched == nil || matched.digest != entry.BaseDigest {
		return mutantCacheEntry{}, false
	}
	// If the mutant's replacement introduced calls to declarations not in the
	// killer's baseline reach, verify the overlaid mutated reach digest directly.
	mutDigest := entry.MutatedDigest
	if len(entry.ExtraDecls) > 0 {
		a, err := sess.checkPkg(overlay, matched.pkgDir, r.platform)
		if err != nil || len(a.Diagnostics) > 0 {
			return mutantCacheEntry{}, false
		}
		curMutDigest, _ := mutatedObligationDigest(a, matched)
		if curMutDigest == "" || curMutDigest != entry.MutatedDigest {
			return mutantCacheEntry{}, false
		}
		mutDigest = curMutDigest
	}
	vk := mutantVerdictKey(declFP, row.From, row.To, row.Decl, row.Refused, entry.KillerPkg, entry.KillerLabel, mutDigest, entry.Mode, r.seed, r.states)
	if _, ok := r.store.Get(vk); !ok {
		return mutantCacheEntry{}, false
	}
	return entry, true
}

func (r *mutantRunner) storeCache(rowKey string, entry mutantCacheEntry, row *mutantRow) {
	if r.store == nil || entry.Killer == "" {
		return
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	r.store.Put(rowKey, string(data))
	vk := mutantVerdictKey(entry.DeclFP, row.From, row.To, row.Decl, row.Refused, entry.KillerPkg, entry.KillerLabel, entry.MutatedDigest, entry.Mode, r.seed, r.states)
	r.store.Put(vk, "ok")
}

type declLineSpan struct {
	name      string
	startByte int
	endByte   int
	startLine int
	endLine   int
}

func enclosingDeclInfo(file string, src []byte, decl string, at, length int) (string, declLineSpan) {
	decls := anchor.ErgoDecls(src)
	if decl != "" {
		for _, d := range decls {
			if d.Name == decl {
				return d.Name, declLineSpan{
					name:      d.Name,
					startByte: d.Start,
					endByte:   d.End,
					startLine: lineAt(src, d.Start),
					endLine:   lineAt(src, d.End),
				}
			}
		}
	}
	for _, d := range decls {
		if d.Start <= at && at+length <= d.End && d.Name != "" {
			return d.Name, declLineSpan{
				name:      d.Name,
				startByte: d.Start,
				endByte:   d.End,
				startLine: lineAt(src, d.Start),
				endLine:   lineAt(src, d.End),
			}
		}
	}
	return "", declLineSpan{
		startByte: 0,
		endByte:   len(src),
		startLine: 0,
		endLine:   0,
	}
}

func declFingerprint(src []byte, span declLineSpan) string {
	text := string(src)
	if span.startByte >= 0 && span.endByte <= len(src) && span.startByte < span.endByte {
		text = string(src[span.startByte:span.endByte])
	}
	norm := syntax.CheckText(text)
	sum := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(sum[:])
}

func lineAt(src []byte, offset int) int {
	if offset < 0 {
		offset = 0
	}
	if offset > len(src) {
		offset = len(src)
	}
	return bytes.Count(src[:offset], []byte{'\n'}) + 1
}

func writeMutantReport(w io.Writer, rep mutantReport) {
	for _, v := range rep.Results {
		switch v.Status {
		case mutantKilled:
			mark := ""
			if v.Cached {
				mark = " (cached)"
			} else if v.Runner != "" {
				mark = " (" + v.Runner + ")"
			}
			fmt.Fprintf(w, "killed   %-40s %s by %s%s\n", v.Name, v.File, v.Killer, mark)
		case mutantSurvived:
			reached := "none"
			if len(v.ReachedBy) > 0 {
				reached = strings.Join(v.ReachedBy, ", ")
			}
			extra := ""
			if v.Detail != "" {
				extra = " (" + v.Detail + ")"
			}
			fmt.Fprintf(w, "SURVIVED %-40s %s [reached: %s]%s\n", v.Name, v.File, reached, extra)
		case mutantInvalid:
			fmt.Fprintf(w, "INVALID  %-40s %s: %s\n", v.Name, v.File, v.Detail)
		case mutantUnjudged:
			fmt.Fprintf(w, "UNJUDGED %-40s %s: %s\n", v.Name, v.File, v.Detail)
		case mutantGap:
			fmt.Fprintf(w, "gap      %-40s %s: %s\n", v.Name, v.File, v.Detail)
		case mutantSkipped:
			fmt.Fprintf(w, "skipped  %-40s %s: %s\n", v.Name, v.File, v.Detail)
		}
	}
	fmt.Fprintf(w, "mutants: %d killed (%d cached), %d survived, %d invalid, %d unjudged, %d gap, %d skipped\n",
		rep.Killed, rep.Cached, rep.Survived, rep.Invalid, rep.Unjudged, rep.Gap, rep.Skipped)
}

func allKillerIDs(row *mutantRow) []string {
	out := append([]string(nil), row.Killers...)
	if row.Anchor != nil {
		out = append(out, row.Anchor.Tests...)
	}
	return out
}

func splitKillerID(id string) (suite, name string) {
	suite, name, ok := strings.Cut(id, ":")
	if !ok {
		return "", ""
	}
	return suite, name
}

func absClean(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved), nil
	}
	return filepath.Clean(abs), nil
}

func isRegularFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}

func dirHasErgo(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".ergo") && !strings.HasPrefix(e.Name(), ".") && !strings.HasPrefix(e.Name(), "_") {
			return true
		}
	}
	return false
}

func pathWithin(p, dir string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(p))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func sha256Sum(b []byte) []byte {
	s := sha256.Sum256(b)
	return s[:]
}
