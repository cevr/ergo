package main

import (
	"bufio"
	"cmp"
	"io"
	"regexp"
	"slices"
	"strings"
)

type StopFingerprint struct {
	Primary   string `json:"primary"`
	Secondary string `json:"secondary,omitempty"`
	Subsystem string `json:"subsystem"`
}

func (f StopFingerprint) String() string {
	if f.Secondary == "" {
		return f.Primary
	}
	return f.Primary + " / " + f.Secondary
}

type StopGroup struct {
	Fingerprint StopFingerprint `json:"fingerprint"`
	Functions   int             `json:"functions"`
	Events      int             `json:"events"`
	Files       int             `json:"files"`
	Sample      string          `json:"sample"`
}

type DiffEntry struct {
	File   string `json:"file"`
	Offset string `json:"offset"`
	Reason string `json:"reason"`
}

type StopLog struct {
	TotalEvents       int
	DistinctFunctions int
	DistinctFiles     int
	Groups            map[StopFingerprint]*StopGroup
	Diffs             []DiffEntry
}

type StopDelta struct {
	Fingerprint    StopFingerprint `json:"fingerprint"`
	BaseFunctions  int             `json:"baseFunctions"`
	HeadFunctions  int             `json:"headFunctions"`
	DeltaFunctions int             `json:"deltaFunctions"`
	BaseEvents     int             `json:"baseEvents"`
	HeadEvents     int             `json:"headEvents"`
	DeltaEvents    int             `json:"deltaEvents"`
}

type StopDiff struct {
	BaseFunctions int         `json:"baseFunctions"`
	HeadFunctions int         `json:"headFunctions"`
	BaseEvents    int         `json:"baseEvents"`
	HeadEvents    int         `json:"headEvents"`
	BaseDiffs     int         `json:"baseDiffs"`
	HeadDiffs     int         `json:"headDiffs"`
	Reduced       []StopDelta `json:"reduced"`
	Regressed     []StopDelta `json:"regressed"`
	NewDiffs      []DiffEntry `json:"newDiffs"`
}

type funcSite struct {
	file   string
	offset string
}

var (
	instanceTypeRE   = regexp.MustCompile(`\binstance\s+[-A-Za-z0-9_.*\[\]]+`)
	foreignSymRE     = regexp.MustCompile(`\bforeign\s+[-A-Za-z0-9_.*\[\]]+`)
	genericForTypeRE = regexp.MustCompile(`\blayout for\s+[-A-Za-z0-9_.*\[\]]+`)
	nameSuffixNumRE  = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)_[0-9]+\b`)
	repoRoots        = []string{"/internal/", "/compiler/", "/examples/", "/modules/", "/hosts/", "/scripts/", "/runtime/", "/cmd/"}
)

func normalizeStopFilePath(p string) string {
	p = strings.ReplaceAll(p, `\`, "/")
	for _, marker := range repoRoots {
		if idx := strings.Index(p, marker); idx >= 0 {
			return p[idx+1:]
		}
	}
	return p
}

func normalizeReasonPart(s string) string {
	s = strings.TrimSpace(s)
	s = instanceTypeRE.ReplaceAllString(s, "instance <T>")
	s = foreignSymRE.ReplaceAllString(s, "foreign <T>")
	s = genericForTypeRE.ReplaceAllString(s, "layout for <T>")
	s = nameSuffixNumRE.ReplaceAllString(s, "${1}_N")
	return s
}

func FingerprintStopReason(reason string) StopFingerprint {
	rawPrimary, rawSecondary, hasSep := strings.Cut(strings.TrimSpace(reason), " / ")
	primary := normalizeReasonPart(rawPrimary)
	secondary := ""
	if hasSep {
		secondary = normalizeReasonPart(rawSecondary)
	}
	return StopFingerprint{
		Primary:   primary,
		Secondary: secondary,
		Subsystem: classifyStopSubsystem(primary, secondary),
	}
}

func classifyStopSubsystem(primary, secondary string) string {
	combined := primary + " " + secondary
	switch {
	case strings.Contains(combined, "SHAPES:"):
		return "shapes"
	case strings.Contains(combined, "UNION:") || strings.HasPrefix(primary, "union:") || strings.Contains(combined, "union layout"):
		return "union"
	case strings.HasPrefix(primary, "signature:"):
		return "signature"
	case strings.HasPrefix(primary, "generic:"):
		return "generic"
	case strings.HasPrefix(primary, "let "):
		return "let"
	case strings.HasPrefix(primary, "match:"):
		return "match"
	case strings.HasPrefix(primary, "callee:") || strings.HasPrefix(primary, "call ") || strings.HasPrefix(primary, "call:"):
		return "call"
	case strings.HasPrefix(primary, "range:"):
		return "range"
	case strings.HasPrefix(primary, "borrow") || strings.Contains(primary, "mut (a borrow)"):
		return "borrow"
	case strings.HasPrefix(primary, "control:"):
		return "control"
	case strings.HasPrefix(primary, "untyped"):
		return "untyped"
	case primary == "extern":
		return "extern"
	case strings.HasPrefix(primary, "parameter:"):
		return "parameter"
	case strings.HasPrefix(primary, "result:"):
		return "result"
	default:
		return "other"
	}
}

func ParseStopLog(r io.Reader) (*StopLog, error) {
	log := &StopLog{
		Groups: map[StopFingerprint]*StopGroup{},
	}
	allFuncs := map[funcSite]bool{}
	allFiles := map[string]bool{}
	groupFuncs := map[StopFingerprint]map[funcSite]bool{}
	groupFiles := map[StopFingerprint]map[string]bool{}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 4)
		if len(parts) < 4 {
			continue
		}
		kind, file, offset, reason := parts[0], normalizeStopFilePath(parts[1]), parts[2], strings.TrimSpace(parts[3])
		switch kind {
		case "S":
			log.TotalEvents++
			site := funcSite{file: file, offset: offset}
			allFuncs[site] = true
			allFiles[file] = true

			fp := FingerprintStopReason(reason)
			g, ok := log.Groups[fp]
			if !ok {
				g = &StopGroup{Fingerprint: fp, Sample: reason}
				log.Groups[fp] = g
				groupFuncs[fp] = map[funcSite]bool{}
				groupFiles[fp] = map[string]bool{}
			}
			g.Events++
			groupFuncs[fp][site] = true
			groupFiles[fp][file] = true
		case "D":
			log.Diffs = append(log.Diffs, DiffEntry{
				File:   file,
				Offset: offset,
				Reason: reason,
			})
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	log.DistinctFunctions = len(allFuncs)
	log.DistinctFiles = len(allFiles)
	for fp, g := range log.Groups {
		g.Functions = len(groupFuncs[fp])
		g.Files = len(groupFiles[fp])
	}
	return log, nil
}

func (l *StopLog) RankedGroups(topN int) []StopGroup {
	out := make([]StopGroup, 0, len(l.Groups))
	for _, g := range l.Groups {
		out = append(out, *g)
	}
	slices.SortFunc(out, func(a, b StopGroup) int {
		if n := cmp.Compare(b.Functions, a.Functions); n != 0 {
			return n
		}
		if n := cmp.Compare(b.Events, a.Events); n != 0 {
			return n
		}
		return compareFingerprint(a.Fingerprint, b.Fingerprint)
	})
	if topN > 0 && len(out) > topN {
		out = out[:topN]
	}
	return out
}

func compareFingerprint(a, b StopFingerprint) int {
	if n := cmp.Compare(a.Subsystem, b.Subsystem); n != 0 {
		return n
	}
	if n := cmp.Compare(a.Primary, b.Primary); n != 0 {
		return n
	}
	return cmp.Compare(a.Secondary, b.Secondary)
}

func DiffStopLogs(base, head *StopLog) StopDiff {
	diff := StopDiff{
		BaseFunctions: base.DistinctFunctions,
		HeadFunctions: head.DistinctFunctions,
		BaseEvents:    base.TotalEvents,
		HeadEvents:    head.TotalEvents,
		BaseDiffs:     len(base.Diffs),
		HeadDiffs:     len(head.Diffs),
	}

	allFPs := map[StopFingerprint]bool{}
	for fp := range base.Groups {
		allFPs[fp] = true
	}
	for fp := range head.Groups {
		allFPs[fp] = true
	}
	for fp := range allFPs {
		var bf, be, hf, he int
		if g := base.Groups[fp]; g != nil {
			bf, be = g.Functions, g.Events
		}
		if g := head.Groups[fp]; g != nil {
			hf, he = g.Functions, g.Events
		}
		df, de := hf-bf, he-be
		if df == 0 && de == 0 {
			continue
		}
		entry := StopDelta{
			Fingerprint:    fp,
			BaseFunctions:  bf,
			HeadFunctions:  hf,
			DeltaFunctions: df,
			BaseEvents:     be,
			HeadEvents:     he,
			DeltaEvents:    de,
		}
		if df < 0 || (df == 0 && de < 0) {
			diff.Reduced = append(diff.Reduced, entry)
		} else {
			diff.Regressed = append(diff.Regressed, entry)
		}
	}

	slices.SortFunc(diff.Reduced, func(a, b StopDelta) int {
		if n := cmp.Compare(a.DeltaFunctions, b.DeltaFunctions); n != 0 {
			return n
		}
		if n := cmp.Compare(a.DeltaEvents, b.DeltaEvents); n != 0 {
			return n
		}
		return compareFingerprint(a.Fingerprint, b.Fingerprint)
	})
	slices.SortFunc(diff.Regressed, func(a, b StopDelta) int {
		if n := cmp.Compare(b.DeltaFunctions, a.DeltaFunctions); n != 0 {
			return n
		}
		if n := cmp.Compare(b.DeltaEvents, a.DeltaEvents); n != 0 {
			return n
		}
		return compareFingerprint(a.Fingerprint, b.Fingerprint)
	})

	baseDiffSet := map[DiffEntry]int{}
	for _, d := range base.Diffs {
		baseDiffSet[d]++
	}
	for _, d := range head.Diffs {
		if baseDiffSet[d] > 0 {
			baseDiffSet[d]--
			continue
		}
		diff.NewDiffs = append(diff.NewDiffs, d)
	}
	return diff
}
