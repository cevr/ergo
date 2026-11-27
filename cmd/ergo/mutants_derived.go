package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"ergo.local/prototype/internal/sketch/mutantderive"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/scripts/mutants/anchor"
)

type derivedTarget struct {
	pkgDir     string
	baseRoot   string
	modRoot    string
	filterFile string
}

type handEditRecord struct {
	rowName  string
	normText string
}

func discoverDerivedTargets(targets []string) ([]derivedTarget, error) {
	seen := map[string]bool{}
	var out []derivedTarget
	add := func(pkgDir, filterFile string) {
		cleanDir := filepath.Clean(pkgDir)
		cleanFile := ""
		if filterFile != "" {
			cleanFile = filepath.Clean(filterFile)
		}
		key := cleanDir + "\x00" + cleanFile
		if seen[key] || !dirHasErgo(cleanDir) {
			return
		}
		seen[key] = true
		repoRoot := findRepoRoot(cleanDir)
		modRoot, _ := moduleRoot(cleanDir)
		baseRoot := repoRoot
		if baseRoot == "" {
			baseRoot = modRoot
		}
		if baseRoot == "" {
			baseRoot = cleanDir
		}
		out = append(out, derivedTarget{
			pkgDir:     cleanDir,
			baseRoot:   baseRoot,
			modRoot:    modRoot,
			filterFile: cleanFile,
		})
	}
	for _, raw := range targets {
		if recursivePattern(raw) {
			base := filepath.Dir(filepath.Clean(raw))
			abs, err := absClean(base)
			if err != nil {
				return nil, err
			}
			pkgs, err := sketchfront.DiscoverPackagesThrough(nil, abs)
			if err != nil {
				return nil, err
			}
			for _, p := range pkgs {
				add(p.Dir, "")
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
			if strings.HasSuffix(abs, ".ergo") {
				add(filepath.Dir(abs), abs)
			}
			continue
		}
		if dirHasErgo(abs) {
			add(abs, "")
			continue
		}
		pkgs, err := sketchfront.DiscoverPackagesThrough(nil, abs)
		if err == nil {
			for _, p := range pkgs {
				add(p.Dir, "")
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].pkgDir != out[j].pkgDir {
			return out[i].pkgDir < out[j].pkgDir
		}
		return out[i].filterFile < out[j].filterFile
	})
	return out, nil
}

func (r *mutantRunner) indexPackagesForDerivedTargets(targets []derivedTarget) {
	indexedRoots := map[string]bool{}
	for _, t := range targets {
		r.loadPkgDir(t.pkgDir, t.baseRoot)
		// Also index sibling packages in the same module so cross-package
		// laws and tests (e.g. modules/board/memory covering modules/board/board)
		// are discovered as killers and covering laws.
		if t.modRoot != "" && !indexedRoots[t.modRoot] {
			indexedRoots[t.modRoot] = true
			r.discoverAndIndexDir(t.modRoot, t.baseRoot)
		}
	}
}

func collectHandEdits(jobs []ledgerJob) map[string][]handEditRecord {
	out := map[string][]handEditRecord{}
	fileCache := map[string][]byte{}
	for _, j := range jobs {
		for _, row := range j.ledger.Mutants {
			if !strings.HasSuffix(row.File, ".ergo") {
				continue
			}
			absFile, _, err := resolveMutantFile(j, row.File)
			if err != nil {
				continue
			}
			cleanAbs := filepath.Clean(absFile)
			if j.filterFile != "" && cleanAbs != filepath.Clean(j.filterFile) {
				continue
			}
			if j.filterDir != "" && !pathWithin(cleanAbs, j.filterDir) {
				continue
			}
			orig, ok := fileCache[cleanAbs]
			if !ok {
				orig, err = os.ReadFile(cleanAbs)
				if err != nil {
					continue
				}
				fileCache[cleanAbs] = orig
			}
			mutated, err := anchor.Apply(row.File, orig, row.Decl, row.From, row.To)
			if err != nil {
				continue
			}
			out[cleanAbs] = append(out[cleanAbs], handEditRecord{
				rowName:  row.Name,
				normText: syntax.CheckText(string(mutated)),
			})
		}
	}
	return out
}

func detectDerivedDuplicates(handEdits map[string][]handEditRecord, derived []mutantderive.Mutant) []mutantDuplicate {
	if len(handEdits) == 0 || len(derived) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var dups []mutantDuplicate
	for _, dm := range derived {
		records := handEdits[filepath.Clean(dm.AbsFile)]
		if len(records) == 0 {
			continue
		}
		dmNorm := syntax.CheckText(dm.Source)
		for _, h := range records {
			if h.normText != dmNorm {
				continue
			}
			key := h.rowName + "\x00" + dm.ID
			if seen[key] {
				continue
			}
			seen[key] = true
			dups = append(dups, mutantDuplicate{
				HandRow:   h.rowName,
				DerivedID: dm.ID,
				File:      dm.File,
				Decl:      dm.Decl,
				Operator:  string(dm.Operator),
				Line:      dm.Line,
			})
		}
	}
	sort.Slice(dups, func(i, j int) bool {
		if dups[i].HandRow != dups[j].HandRow {
			return dups[i].HandRow < dups[j].HandRow
		}
		return dups[i].DerivedID < dups[j].DerivedID
	})
	return dups
}

func (r *mutantRunner) runDerivedMutants(targets []derivedTarget, jobs []ledgerJob, nameRe *regexp.Regexp, rep *mutantReport) {
	rep.DerivedMode = true
	handEdits := collectHandEdits(jobs)

	var mu sync.Mutex
	var lookupViable func(string) (bool, bool)
	var storeViable func(string, bool)
	if r.store != nil && !r.opts.fresh {
		lookupViable = func(key string) (bool, bool) {
			mu.Lock()
			defer mu.Unlock()
			val, ok := r.store.Get(key)
			if !ok {
				return false, false
			}
			return val == "1", true
		}
		storeViable = func(key string, viable bool) {
			mu.Lock()
			defer mu.Unlock()
			val := "0"
			if viable {
				val = "1"
			}
			r.store.Put(key, val)
		}
	}

	var allDerived []mutantderive.Mutant
	type derivedItem struct {
		m        mutantderive.Mutant
		baseRoot string
	}
	var items []derivedItem
	for _, t := range targets {
		info := r.loadPkgDir(t.pkgDir, t.baseRoot)
		if info == nil {
			continue
		}
		muts := mutantderive.GeneratePackage(t.pkgDir, r.platform, info.analysis, mutantderive.Options{
			BaseRoot:     t.baseRoot,
			FilterFile:   t.filterFile,
			LookupViable: lookupViable,
			StoreViable:  storeViable,
		})
		var skipRe *regexp.Regexp
		if r.opts.skip != "" {
			skipRe, _ = regexp.Compile(r.opts.skip)
		}
		for _, m := range muts {
			allDerived = append(allDerived, m)
			if nameRe != nil && !nameRe.MatchString(m.ID) {
				continue
			}
			if skipRe != nil && skipRe.MatchString(m.ID) {
				continue
			}
			items = append(items, derivedItem{m: m, baseRoot: t.baseRoot})
		}
	}

	rep.Duplicates = detectDerivedDuplicates(handEdits, allDerived)
	fileCache := map[string][]byte{}
	for _, item := range items {
		rep.DerivedGenerated++
		v := r.judgeDerived(item.m, fileCache)
		rep.Results = append(rep.Results, v)
		switch v.Status {
		case mutantKilled:
			rep.DerivedKilled++
			if v.Cached {
				rep.DerivedCached++
			}
		case mutantSurvived:
			rep.DerivedSurvived++
			if v.LawCovered {
				rep.DerivedLawSurvived++
			}
		case mutantInvalid:
			rep.DerivedInvalid++
		case mutantUnjudged:
			rep.DerivedUnjudged++
		}
	}
}

func (r *mutantRunner) judgeDerived(dm mutantderive.Mutant, fileCache map[string][]byte) mutantVerdict {
	start := time.Now()
	v := mutantVerdict{
		Name:     dm.ID,
		File:     dm.File,
		Decl:     dm.Decl,
		Derived:  true,
		Operator: string(dm.Operator),
		Line:     dm.Line,
	}
	defer func() {
		v.DurationMs = time.Since(start).Milliseconds()
	}()

	cleanAbs := filepath.Clean(dm.AbsFile)
	orig, ok := fileCache[cleanAbs]
	if !ok {
		data, err := os.ReadFile(cleanAbs)
		if err != nil {
			v.Status = mutantInvalid
			v.Detail = err.Error()
			return v
		}
		orig = data
		fileCache[cleanAbs] = orig
	}

	declSpan := declSpanForDerived(orig, dm.Decl, dm.Line)
	declFP := declFingerprint(orig, declSpan)
	mutPkgDir := filepath.Dir(cleanAbs)
	mutPkgPath := mutPkgDir
	if info := r.pkgLoaded[mutPkgDir]; info != nil {
		mutPkgPath = info.path
	}

	reachObs := r.reachingObligations(cleanAbs, declSpan, mutPkgDir)
	if dm.Operator == mutantderive.OpWeakenLawPremise {
		var selfLaws []*mutantObligation
		for _, ob := range r.allObs {
			if ob.kind == "law" && lawNameMatches(ob.name, ob.label, dm.Decl) {
				selfLaws = append(selfLaws, ob)
			}
		}
		sortObligations(selfLaws)
		reachObs = mergeObligations(selfLaws, reachObs)
	}

	var coveringLaws []string
	for _, ob := range reachObs {
		if ob.kind == "law" {
			coveringLaws = append(coveringLaws, ob.id)
		}
	}
	slices.Sort(coveringLaws)
	coveringLaws = slices.Compact(coveringLaws)
	v.LawCovered = len(coveringLaws) > 0
	v.CoveringLaws = coveringLaws

	overlay := sketchfront.OverlayFS(map[string]string{cleanAbs: dm.Source})
	sess := newRowSession()
	defer sess.close()

	// Synthetic row for reach-digest verdict cache sharing.
	srcSum := sha256.Sum256([]byte(dm.Source))
	synthRow := &mutantRow{
		Name: dm.ID,
		File: dm.File,
		Decl: dm.Decl,
		From: fmt.Sprintf("derived:%s:L%d", dm.Operator, dm.Line),
		To:   hex.EncodeToString(srcSum[:]),
	}
	modePref := r.modeTag("")
	rowKey := mutantRowKey(mutPkgPath, dm.File, dm.Decl, declFP, synthRow.From, synthRow.To, "", modePref, r.seed, r.states)
	survKey := derivedSurvivedCacheKey(rowKey, reachObs)
	if !r.opts.fresh {
		if cached, ok := r.lookupCache(rowKey, declFP, synthRow, overlay, sess, reachObs, nil); ok {
			v.Status = mutantKilled
			v.Killer = cached.Killer
			v.Runner = "cached"
			v.Cached = true
			return v
		}
		if r.store != nil {
			if _, ok := r.store.Get(survKey); ok {
				v.Status = mutantSurvived
				v.Cached = true
				for _, ob := range reachObs {
					v.ReachedBy = append(v.ReachedBy, ob.id)
				}
				if v.LawCovered {
					v.Detail = fmt.Sprintf("derived %s survived in law-covered %s (line %d)", dm.Operator, dm.Decl, dm.Line)
				}
				return v
			}
		}
	}

	var reachedPassed []string
	var baselineReds []string
	timedOut := false
	ranAny := false

candidateLoop:
	for _, ob := range reachObs {
		base := r.checkBaseline(ob)
		if !base.passed {
			baselineReds = append(baselineReds, ob.id+" ("+base.detail+")")
			continue
		}
		ranAny = true
		// Guard against an unviable derived mutant ever counting as killed by a check failure
		// in its own package.
		if ob.pkgDir == mutPkgDir {
			if a, err := sess.checkPkg(overlay, ob.pkgDir, r.platform); err != nil || len(a.Diagnostics) > 0 {
				v.Status = mutantInvalid
				if err != nil {
					v.Detail = err.Error()
				} else {
					v.Detail = fmt.Sprintf("derived mutant introduced checker diagnostic %s", a.Diagnostics[0].Code)
				}
				return v
			}
		}
		outcome, runnerKind, mutDigest, extraDecls := r.runObligationOnMutant(ob, overlay, sess, base.duration, false)
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
			}, synthRow)
			return v
		case "timeout":
			timedOut = true
			break candidateLoop
		case "passed":
			reachedPassed = append(reachedPassed, ob.id)
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
	case len(baselineReds) > 0 && len(reachedPassed) == 0:
		v.Status = mutantUnjudged
		v.Detail = "baseline failed: " + strings.Join(baselineReds, "; ")
	default:
		v.Status = mutantSurvived
		if v.LawCovered {
			v.Detail = fmt.Sprintf("derived %s survived in law-covered %s (line %d)", dm.Operator, dm.Decl, dm.Line)
		}
		if r.store != nil {
			r.store.Put(survKey, "survived")
		}
	}
	return v
}

func derivedSurvivedCacheKey(rowKey string, reachObs []*mutantObligation) string {
	h := sha256.New()
	fmt.Fprintf(h, "derived-survived-v1\x00%s\x00", rowKey)
	for _, ob := range reachObs {
		fmt.Fprintf(h, "%s\x00%s\x00", ob.id, ob.digest)
	}
	return "ms:" + hex.EncodeToString(h.Sum(nil))
}

func declSpanForDerived(src []byte, declName string, line int) declLineSpan {
	decls := anchor.ErgoDecls(src)
	for _, d := range decls {
		sl := lineAt(src, d.Start)
		el := lineAt(src, d.End)
		if line >= sl && line <= el {
			name := d.Name
			if declName != "" {
				name = declName
			}
			return declLineSpan{
				name:      name,
				startByte: d.Start,
				endByte:   d.End,
				startLine: sl,
				endLine:   el,
			}
		}
	}
	if declName != "" {
		for _, d := range decls {
			if d.Name == declName {
				return declLineSpan{
					name:      d.Name,
					startByte: d.Start,
					endByte:   d.End,
					startLine: lineAt(src, d.Start),
					endLine:   lineAt(src, d.End),
				}
			}
		}
	}
	return declLineSpan{
		name:      declName,
		startByte: 0,
		endByte:   len(src),
		startLine: 0,
		endLine:   0,
	}
}

func writeDerivedReportExtras(w io.Writer, rep mutantReport) {
	for _, dup := range rep.Duplicates {
		fmt.Fprintf(w, "duplicate %s matches derived %s (%s %s %s:%d)\n",
			dup.HandRow, dup.DerivedID, dup.Decl, dup.Operator, dup.File, dup.Line)
	}
	if rep.DerivedMode {
		fmt.Fprintf(w, "derived: %d generated, %d killed (%d cached), %d survived (%d in law-covered decls), %d invalid, %d unjudged, %d duplicates\n",
			rep.DerivedGenerated, rep.DerivedKilled, rep.DerivedCached, rep.DerivedSurvived, rep.DerivedLawSurvived, rep.DerivedInvalid, rep.DerivedUnjudged, len(rep.Duplicates))
	}
}
