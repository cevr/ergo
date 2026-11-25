package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketchfront"
)

type propertyMeta struct {
	Key    string
	Digest string
}

func casesFilePath(target string) string {
	ledger := ledgerPath(target)
	if ledger == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(ledger), lawledger.CasesFile)
}

func normalizeDigest(d string) string {
	if strings.HasPrefix(d, "h1:") {
		return d
	}
	return "h1:" + d
}

func propertyMetas(pkg *check.Package) map[string]propertyMeta {
	out := map[string]propertyMeta{}
	if pkg == nil {
		return out
	}
	for _, t := range pkg.Tests {
		if len(t.Params) == 0 {
			continue
		}
		out[t.Name] = propertyMeta{
			Key:    lawledger.PropertyKey(pkg.Path, t.Name),
			Digest: normalizeDigest(lower.TestDigest(pkg, t)),
		}
	}
	for _, rt := range lower.RunTargets(pkg) {
		out[rt.Label] = propertyMeta{
			Key:    lawledger.PropertyKey(pkg.Path, rt.Label),
			Digest: normalizeDigest(rt.Digest(pkg)),
		}
	}
	return out
}

func loadStoredCasesForPkg(target string, pkg *check.Package) (map[string][]eval.StoredCase, error) {
	if isStdTarget(target) || pkg == nil {
		return nil, nil
	}
	path := casesFilePath(target)
	if path == "" {
		return nil, nil
	}
	store, err := lawledger.ReadCasesFile(path)
	if err != nil {
		return nil, err
	}
	if len(store.Property) == 0 {
		return nil, nil
	}
	metas := propertyMetas(pkg)
	out := map[string][]eval.StoredCase{}
	for label, meta := range metas {
		entries := store.ForProperty(meta.Key)
		if len(entries) == 0 {
			continue
		}
		list := make([]eval.StoredCase, len(entries))
		for i, e := range entries {
			list[i] = eval.StoredCase{
				Key:    e.Key,
				Digest: e.Digest,
				Tape:   e.Tape,
				Stale:  e.Digest != meta.Digest,
			}
		}
		out[label] = list
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// updateCasesStore records any newly refuted shrunk tapes into ergo.cases and,
// only when prune is true (`ergo test --prune-cases`), removes stored cases
// that passed and orphaned entries for deleted properties.
func updateCasesStore(target string, pkg *check.Package, tapes []sketchfront.RefutedTape, passed map[string][]int, prune bool) (pruned, kept int, err error) {
	if isStdTarget(target) || pkg == nil {
		return 0, 0, nil
	}
	if !prune && len(tapes) == 0 && len(passed) == 0 {
		return 0, 0, nil
	}
	path := casesFilePath(target)
	if path == "" {
		return 0, 0, nil
	}
	store, err := lawledger.ReadCasesFile(path)
	if err != nil {
		return 0, 0, err
	}
	metas := propertyMetas(pkg)
	changed := false
	for _, rt := range tapes {
		meta, ok := metas[rt.Name]
		if !ok {
			continue
		}
		if store.AddProperty(lawledger.PropertyCase{
			Key:    meta.Key,
			Digest: meta.Digest,
			Tape:   rt.Tape,
		}) {
			changed = true
		}
	}
	if prune && len(passed) > 0 {
		for label, passedIdxs := range passed {
			meta, ok := metas[label]
			if !ok || len(passedIdxs) == 0 {
				continue
			}
			passSet := map[int]bool{}
			for _, idx := range passedIdxs {
				passSet[idx] = true
			}
			curIdx := 0
			pruned += store.RemoveProperty(meta.Key, func(lawledger.PropertyCase) bool {
				hit := passSet[curIdx]
				curIdx++
				return hit
			})
		}
	}
	if prune && pkg.Path != "" {
		liveKeys := map[string]bool{}
		for _, meta := range metas {
			liveKeys[meta.Key] = true
		}
		pruned += store.PruneOrphans(pkg.Path+".", liveKeys)
	}
	if changed || pruned > 0 {
		if err := lawledger.WriteCasesFile(path, store); err != nil {
			return 0, 0, err
		}
	}
	return pruned, len(store.Property), nil
}

// recordRefutedTapes appends shrunk tapes of refuted properties to ergo.cases.
// Existing passing entries are kept untouched until `ergo test --prune-cases`.
func recordRefutedTapes(target string, pkg *check.Package, tapes []sketchfront.RefutedTape) error {
	_, _, err := updateCasesStore(target, pkg, tapes, nil, false)
	return err
}

func writeTapesTempFile(stored map[string][]eval.StoredCase) (dir, file string, err error) {
	dir, err = os.MkdirTemp("", "ergo-tapes-")
	if err != nil {
		return "", "", err
	}
	file = filepath.Join(dir, "tapes")
	var b strings.Builder
	labels := make([]string, 0, len(stored))
	for label := range stored {
		labels = append(labels, label)
	}
	slices.Sort(labels)
	for _, label := range labels {
		for _, sc := range stored[label] {
			staleFlag := "0"
			if sc.Stale {
				staleFlag = "1"
			}
			b.WriteString(label + "\t" + staleFlag + "\t" + sc.Digest + "\t" + lawledger.EncodeTape(sc.Tape) + "\n")
		}
	}
	if err := os.WriteFile(file, []byte(b.String()), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", "", err
	}
	return dir, file, nil
}

// tapeLines reads machine-readable `ergo:tape` and `ergo:tape-pass` lines
// emitted by a native test binary on stderr while passing every other line
// through to next.
type tapeLines struct {
	next     io.Writer
	part     []byte
	recorded []sketchfront.RefutedTape
	passed   map[string][]int
}

func (t *tapeLines) Write(p []byte) (int, error) {
	t.part = append(t.part, p...)
	for {
		i := bytes.IndexByte(t.part, '\n')
		if i < 0 {
			break
		}
		rawLine := string(t.part[:i])
		t.part = t.part[i+1:]
		line := strings.TrimRight(rawLine, "\r")
		if rest, ok := strings.CutPrefix(line, "ergo:tape "); ok {
			if label, enc, ok2 := strings.Cut(rest, "\t"); ok2 {
				if tape, err := lawledger.DecodeTape(enc); err == nil {
					t.recorded = append(t.recorded, sketchfront.RefutedTape{Name: label, Tape: tape})
				}
			}
			continue
		}
		if rest, ok := strings.CutPrefix(line, "ergo:tape-pass "); ok {
			if label, idxText, ok2 := strings.Cut(rest, "\t"); ok2 {
				if idx, err := strconv.Atoi(idxText); err == nil {
					if t.passed == nil {
						t.passed = map[string][]int{}
					}
					t.passed[label] = append(t.passed[label], idx)
				}
			}
			continue
		}
		if t.next != nil {
			_, _ = io.WriteString(t.next, rawLine+"\n")
		}
	}
	return len(p), nil
}

func (t *tapeLines) Flush() {
	if len(t.part) > 0 && t.next != nil {
		_, _ = t.next.Write(t.part)
	}
	t.part = nil
}

func filterCachedForStored(text string, stored map[string][]eval.StoredCase) string {
	if text == "" || len(stored) == 0 {
		return text
	}
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			continue
		}
		label, _, ok := strings.Cut(line, "\t")
		if ok && len(stored[label]) > 0 {
			continue
		}
		out = append(out, line)
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, "\n") + "\n"
}

func pruneCasesForLoaded(opts fileOptions, l loaded, stdout, stderr io.Writer) error {
	pkg := l.analysis.Root().Package
	if pkg == nil || isStdTarget(opts.file) {
		return nil
	}
	storedCases, err := loadStoredCasesForPkg(opts.file, pkg)
	if err != nil {
		return err
	}

	passedByLabel := map[string][]int{}
	if len(storedCases) > 0 {
		decided := false
		if !opts.native {
			seed, states := uint64(1), 0
			if opts.seed != "" {
				seed, _ = strconv.ParseUint(opts.seed, 10, 64)
			}
			if opts.states != "" {
				states, _ = strconv.Atoi(opts.states)
			}
			ans, ok := l.analysis.EvaluateTestsWithCases(seed, states, l.source.Admitted(), opts.file, nil, "", storedCases, true)
			if ok {
				passedByLabel = ans.PassedStored
				decided = true
			}
		}
		if !decided {
			passedByLabel, err = pruneCasesNative(opts, l, storedCases)
			if err != nil {
				return err
			}
		}
	}

	pruned, kept, err := updateCasesStore(opts.file, pkg, nil, passedByLabel, true)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s: pruned %d passing case(s) (%d kept)\n", lawledger.CasesFile, pruned, kept)
	return nil
}

func pruneCasesNative(opts fileOptions, l loaded, storedCases map[string][]eval.StoredCase) (map[string][]int, error) {
	dir, err := os.MkdirTemp("", "ergo-prune-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	binary := filepath.Join(dir, l.name+".test")
	root, err := build.DefaultRoot()
	if err != nil {
		return nil, err
	}
	tc, err := buildAction(opts)
	if err != nil {
		return nil, err
	}
	req := emissionRequest(opts, l, build.ModeTest)
	req.Output, req.Cache, req.Toolchain, req.Stderr = binary, build.Cache{Root: root}, tc, io.Discard
	if _, err := sketchfront.Build(l.analysis, req); err != nil {
		return nil, err
	}
	tapesDir, tapesFile, err := writeTapesTempFile(storedCases)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tapesDir)
	childArgs, err := testChildArgs(opts, l.result.Revision)
	if err != nil {
		return nil, err
	}
	childArgs = append(childArgs, "--tapes", tapesFile, "--prune-tapes")
	limit, err := proclimit.Limit(os.Environ(), proclimit.DefaultLimit)
	if err != nil {
		return nil, err
	}
	child := testProgramCommand(binary, childArgs, limit)
	tapes := &tapeLines{}
	child.Stdout = io.Discard
	child.Stderr = tapes
	_ = runEntryProcess(child, limit, proclimit.Watch(child))
	return tapes.passed, nil
}
