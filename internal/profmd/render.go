package profmd

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Options controls Markdown profile rendering. Zero fields fall back to
// defaults (Top=25, TopStacks=10).
type Options struct {
	Top       int
	TopStacks int
}

const (
	defaultTop       = 25
	defaultTopStacks = 10
	maxStackDepth    = 6
)

func normalizeOptions(opts Options) Options {
	if opts.Top <= 0 {
		opts.Top = defaultTop
	}
	if opts.TopStacks <= 0 {
		opts.TopStacks = defaultTopStacks
	}
	return opts
}

type resolvedFrame struct {
	key      string
	name     string
	location string
}

type cpuFuncStat struct {
	key      string
	name     string
	location string
	self     int64
	cum      int64
}

type stackStat struct {
	stack string
	value int64
}

type heapFuncStat struct {
	key          string
	name         string
	location     string
	allocSpace   int64
	allocObjects int64
	cumAlloc     int64
	inuseSpace   int64
	inuseObjects int64
	cumInuse     int64
}

// RenderCPUMarkdown decodes a pprof CPU profile and renders an agent-readable
// Markdown report with summary metrics, top functions by self time, top
// functions by cumulative time, and hot call stacks.
func RenderCPUMarkdown(pprofBytes []byte, opts Options) (string, error) {
	p, err := ParseProfile(pprofBytes)
	if err != nil {
		return "", err
	}
	opts = normalizeOptions(opts)

	cpuIdx := findSampleType(p, "cpu")
	if cpuIdx < 0 {
		cpuIdx = len(p.SampleType) - 1
	}
	samplesIdx := findSampleType(p, "samples")
	isNanos := p.SampleType[cpuIdx].Unit == "nanoseconds"

	var totalCPU int64
	var totalSamples int64
	funcs := make(map[string]*cpuFuncStat)
	stacks := make(map[string]int64)

	for _, s := range p.Sample {
		if samplesIdx >= 0 && samplesIdx < len(s.Value) {
			totalSamples += s.Value[samplesIdx]
		}
		if cpuIdx >= len(s.Value) {
			continue
		}
		v := s.Value[cpuIdx]
		if v <= 0 {
			continue
		}
		totalCPU += v

		frames := resolveFrames(p, s.LocationID)
		if len(frames) == 0 {
			continue
		}

		// Self time accrues to the leaf (innermost) frame.
		leaf := frames[0]
		leafStat := getOrCreateCPUStat(funcs, leaf)
		leafStat.self += v

		// Cumulative time accrues once per unique function in the sample stack
		// so recursive frames are never double-counted.
		seen := make(map[string]struct{}, len(frames))
		for _, f := range frames {
			if _, dup := seen[f.key]; dup {
				continue
			}
			seen[f.key] = struct{}{}
			st := getOrCreateCPUStat(funcs, f)
			st.cum += v
		}

		stackKey := formatHotStack(frames)
		stacks[stackKey] += v
	}

	if samplesIdx < 0 {
		totalSamples = int64(len(p.Sample))
	}

	bySelf := make([]*cpuFuncStat, 0, len(funcs))
	byCum := make([]*cpuFuncStat, 0, len(funcs))
	for _, st := range funcs {
		if st.self > 0 {
			bySelf = append(bySelf, st)
		}
		if st.cum > 0 {
			byCum = append(byCum, st)
		}
	}

	sort.Slice(bySelf, func(i, j int) bool {
		if bySelf[i].self != bySelf[j].self {
			return bySelf[i].self > bySelf[j].self
		}
		if bySelf[i].cum != bySelf[j].cum {
			return bySelf[i].cum > bySelf[j].cum
		}
		if bySelf[i].name != bySelf[j].name {
			return bySelf[i].name < bySelf[j].name
		}
		return bySelf[i].location < bySelf[j].location
	})
	sort.Slice(byCum, func(i, j int) bool {
		if byCum[i].cum != byCum[j].cum {
			return byCum[i].cum > byCum[j].cum
		}
		if byCum[i].self != byCum[j].self {
			return byCum[i].self > byCum[j].self
		}
		if byCum[i].name != byCum[j].name {
			return byCum[i].name < byCum[j].name
		}
		return byCum[i].location < byCum[j].location
	})

	if len(bySelf) > opts.Top {
		bySelf = bySelf[:opts.Top]
	}
	if len(byCum) > opts.Top {
		byCum = byCum[:opts.Top]
	}

	hotStacks := make([]stackStat, 0, len(stacks))
	for k, v := range stacks {
		hotStacks = append(hotStacks, stackStat{stack: k, value: v})
	}
	sort.Slice(hotStacks, func(i, j int) bool {
		if hotStacks[i].value != hotStacks[j].value {
			return hotStacks[i].value > hotStacks[j].value
		}
		return hotStacks[i].stack < hotStacks[j].stack
	})
	if len(hotStacks) > opts.TopStacks {
		hotStacks = hotStacks[:opts.TopStacks]
	}

	fmtVal := func(v int64) string {
		if isNanos {
			return formatDurationNanos(v)
		}
		return fmt.Sprintf("%d %s", v, p.SampleType[cpuIdx].Unit)
	}

	var b strings.Builder
	b.WriteString("# CPU Profile\n\n")
	fmt.Fprintf(&b, "- **Duration:** %s\n", formatDurationNanos(p.DurationNanos))
	fmt.Fprintf(&b, "- **Total CPU:** %s\n", fmtVal(totalCPU))
	fmt.Fprintf(&b, "- **Samples:** %d\n", totalSamples)
	if p.Period > 0 {
		if p.PeriodType.Unit == "nanoseconds" {
			hz := 1e9 / float64(p.Period)
			fmt.Fprintf(&b, "- **Sample Period:** %s (%.0f Hz)\n", formatDurationNanos(p.Period), hz)
		} else {
			fmt.Fprintf(&b, "- **Sample Period:** %d %s\n", p.Period, p.PeriodType.Unit)
		}
	}
	fmt.Fprintf(&b, "- **Unique Functions:** %d\n", len(funcs))

	b.WriteString("\n## Top Functions by Self Time\n\n")
	if len(bySelf) == 0 {
		b.WriteString("_No CPU samples recorded._\n")
	} else {
		b.WriteString("| Rank | Self% | Self | Cum% | Cum | Function | Location |\n")
		b.WriteString("| ---: | ---: | ---: | ---: | ---: | --- | --- |\n")
		for i, st := range bySelf {
			fmt.Fprintf(&b, "| %d | %s | %s | %s | %s | `%s` | `%s` |\n",
				i+1,
				formatPct(st.self, totalCPU),
				fmtVal(st.self),
				formatPct(st.cum, totalCPU),
				fmtVal(st.cum),
				escapeMarkdownCell(st.name),
				escapeMarkdownCell(st.location),
			)
		}
	}

	b.WriteString("\n## Top Functions by Cumulative Time\n\n")
	if len(byCum) == 0 {
		b.WriteString("_No CPU samples recorded._\n")
	} else {
		b.WriteString("| Rank | Cum% | Cum | Self% | Self | Function | Location |\n")
		b.WriteString("| ---: | ---: | ---: | ---: | ---: | --- | --- |\n")
		for i, st := range byCum {
			fmt.Fprintf(&b, "| %d | %s | %s | %s | %s | `%s` | `%s` |\n",
				i+1,
				formatPct(st.cum, totalCPU),
				fmtVal(st.cum),
				formatPct(st.self, totalCPU),
				fmtVal(st.self),
				escapeMarkdownCell(st.name),
				escapeMarkdownCell(st.location),
			)
		}
	}

	b.WriteString("\n## Hot Call Stacks (caller -> leaf)\n\n")
	if len(hotStacks) == 0 {
		b.WriteString("_No call stacks recorded._\n")
	} else {
		b.WriteString("| Rank | % | Time | Stack |\n")
		b.WriteString("| ---: | ---: | ---: | --- |\n")
		for i, st := range hotStacks {
			fmt.Fprintf(&b, "| %d | %s | %s | `%s` |\n",
				i+1,
				formatPct(st.value, totalCPU),
				fmtVal(st.value),
				escapeMarkdownCell(st.stack),
			)
		}
	}

	return b.String(), nil
}

// RenderHeapMarkdown decodes a pprof heap profile and renders an agent-readable
// Markdown report with summary totals, top allocators by bytes (alloc_space),
// and top live heap retainers (inuse_space).
func RenderHeapMarkdown(pprofBytes []byte, opts Options) (string, error) {
	p, err := ParseProfile(pprofBytes)
	if err != nil {
		return "", err
	}
	opts = normalizeOptions(opts)

	allocObjIdx := findSampleType(p, "alloc_objects")
	allocSpaceIdx := findSampleType(p, "alloc_space")
	inuseObjIdx := findSampleType(p, "inuse_objects")
	inuseSpaceIdx := findSampleType(p, "inuse_space")
	if allocSpaceIdx < 0 && inuseSpaceIdx < 0 {
		return "", errors.New("profmd: heap profile missing alloc_space and inuse_space sample types")
	}

	var totalAllocSpace int64
	var totalAllocObjects int64
	var totalInuseSpace int64
	var totalInuseObjects int64
	funcs := make(map[string]*heapFuncStat)

	for _, s := range p.Sample {
		allocSpace := sampleVal(s, allocSpaceIdx)
		allocObj := sampleVal(s, allocObjIdx)
		inuseSpace := sampleVal(s, inuseSpaceIdx)
		inuseObj := sampleVal(s, inuseObjIdx)

		totalAllocSpace += allocSpace
		totalAllocObjects += allocObj
		totalInuseSpace += inuseSpace
		totalInuseObjects += inuseObj

		if allocSpace <= 0 && allocObj <= 0 && inuseSpace <= 0 && inuseObj <= 0 {
			continue
		}

		frames := resolveFrames(p, s.LocationID)
		if len(frames) == 0 {
			continue
		}

		leaf := frames[0]
		leafStat := getOrCreateHeapStat(funcs, leaf)
		leafStat.allocSpace += allocSpace
		leafStat.allocObjects += allocObj
		leafStat.inuseSpace += inuseSpace
		leafStat.inuseObjects += inuseObj

		seen := make(map[string]struct{}, len(frames))
		for _, f := range frames {
			if _, dup := seen[f.key]; dup {
				continue
			}
			seen[f.key] = struct{}{}
			st := getOrCreateHeapStat(funcs, f)
			st.cumAlloc += allocSpace
			st.cumInuse += inuseSpace
		}
	}

	byAlloc := make([]*heapFuncStat, 0, len(funcs))
	byInuse := make([]*heapFuncStat, 0, len(funcs))
	for _, st := range funcs {
		if st.allocSpace > 0 {
			byAlloc = append(byAlloc, st)
		}
		if st.inuseSpace > 0 {
			byInuse = append(byInuse, st)
		}
	}

	sort.Slice(byAlloc, func(i, j int) bool {
		if byAlloc[i].allocSpace != byAlloc[j].allocSpace {
			return byAlloc[i].allocSpace > byAlloc[j].allocSpace
		}
		if byAlloc[i].cumAlloc != byAlloc[j].cumAlloc {
			return byAlloc[i].cumAlloc > byAlloc[j].cumAlloc
		}
		if byAlloc[i].allocObjects != byAlloc[j].allocObjects {
			return byAlloc[i].allocObjects > byAlloc[j].allocObjects
		}
		if byAlloc[i].name != byAlloc[j].name {
			return byAlloc[i].name < byAlloc[j].name
		}
		return byAlloc[i].location < byAlloc[j].location
	})
	sort.Slice(byInuse, func(i, j int) bool {
		if byInuse[i].inuseSpace != byInuse[j].inuseSpace {
			return byInuse[i].inuseSpace > byInuse[j].inuseSpace
		}
		if byInuse[i].cumInuse != byInuse[j].cumInuse {
			return byInuse[i].cumInuse > byInuse[j].cumInuse
		}
		if byInuse[i].inuseObjects != byInuse[j].inuseObjects {
			return byInuse[i].inuseObjects > byInuse[j].inuseObjects
		}
		if byInuse[i].name != byInuse[j].name {
			return byInuse[i].name < byInuse[j].name
		}
		return byInuse[i].location < byInuse[j].location
	})

	if len(byAlloc) > opts.Top {
		byAlloc = byAlloc[:opts.Top]
	}
	if len(byInuse) > opts.Top {
		byInuse = byInuse[:opts.Top]
	}

	var b strings.Builder
	b.WriteString("# Heap Profile\n\n")
	fmt.Fprintf(&b, "- **Alloc Space:** %s (%d B)\n", formatBytes(totalAllocSpace), totalAllocSpace)
	fmt.Fprintf(&b, "- **Alloc Objects:** %d\n", totalAllocObjects)
	fmt.Fprintf(&b, "- **In-Use Space:** %s (%d B)\n", formatBytes(totalInuseSpace), totalInuseSpace)
	fmt.Fprintf(&b, "- **In-Use Objects:** %d\n", totalInuseObjects)
	fmt.Fprintf(&b, "- **Unique Functions:** %d\n", len(funcs))

	b.WriteString("\n## Top Allocators by Bytes (alloc_space)\n\n")
	if len(byAlloc) == 0 {
		b.WriteString("_No allocations recorded._\n")
	} else {
		b.WriteString("| Rank | % | Alloc Bytes | Cum Bytes | Alloc Objects | Function | Location |\n")
		b.WriteString("| ---: | ---: | ---: | ---: | ---: | --- | --- |\n")
		for i, st := range byAlloc {
			fmt.Fprintf(&b, "| %d | %s | %s | %s | %d | `%s` | `%s` |\n",
				i+1,
				formatPct(st.allocSpace, totalAllocSpace),
				formatBytes(st.allocSpace),
				formatBytes(st.cumAlloc),
				st.allocObjects,
				escapeMarkdownCell(st.name),
				escapeMarkdownCell(st.location),
			)
		}
	}

	b.WriteString("\n## Top Live Heap (inuse_space)\n\n")
	if len(byInuse) == 0 {
		b.WriteString("_No live heap objects recorded._\n")
	} else {
		b.WriteString("| Rank | % | In-Use Bytes | Cum Bytes | In-Use Objects | Function | Location |\n")
		b.WriteString("| ---: | ---: | ---: | ---: | ---: | --- | --- |\n")
		for i, st := range byInuse {
			fmt.Fprintf(&b, "| %d | %s | %s | %s | %d | `%s` | `%s` |\n",
				i+1,
				formatPct(st.inuseSpace, totalInuseSpace),
				formatBytes(st.inuseSpace),
				formatBytes(st.cumInuse),
				st.inuseObjects,
				escapeMarkdownCell(st.name),
				escapeMarkdownCell(st.location),
			)
		}
	}

	return b.String(), nil
}

func findSampleType(p *Profile, name string) int {
	for i, st := range p.SampleType {
		if st.Type == name {
			return i
		}
	}
	return -1
}

func sampleVal(s Sample, idx int) int64 {
	if idx < 0 || idx >= len(s.Value) {
		return 0
	}
	return s.Value[idx]
}

func resolveFrames(p *Profile, locIDs []uint64) []resolvedFrame {
	var frames []resolvedFrame
	for _, locID := range locIDs {
		loc := p.locationByID[locID]
		if loc == nil || len(loc.Line) == 0 {
			frames = append(frames, resolvedFrame{
				key:      fmt.Sprintf("<unknown@%d>", locID),
				name:     "<unknown>",
				location: fmt.Sprintf("0x%x", locID),
			})
			continue
		}
		for _, ln := range loc.Line {
			fn := p.functionByID[ln.FunctionID]
			name := "<unknown>"
			file := ""
			lineNum := ln.Line
			if fn != nil {
				if fn.Name != "" {
					name = fn.Name
				} else if fn.SystemName != "" {
					name = fn.SystemName
				}
				file = fn.Filename
				if lineNum == 0 {
					lineNum = fn.StartLine
				}
			}
			locStr := formatLocation(file, lineNum)
			key := name + "\x00" + shortenPath(file)
			frames = append(frames, resolvedFrame{
				key:      key,
				name:     name,
				location: locStr,
			})
		}
	}
	return frames
}

func getOrCreateCPUStat(m map[string]*cpuFuncStat, f resolvedFrame) *cpuFuncStat {
	st, ok := m[f.key]
	if !ok {
		st = &cpuFuncStat{
			key:      f.key,
			name:     f.name,
			location: f.location,
		}
		m[f.key] = st
	}
	return st
}

func getOrCreateHeapStat(m map[string]*heapFuncStat, f resolvedFrame) *heapFuncStat {
	st, ok := m[f.key]
	if !ok {
		st = &heapFuncStat{
			key:      f.key,
			name:     f.name,
			location: f.location,
		}
		m[f.key] = st
	}
	return st
}

func formatHotStack(frames []resolvedFrame) string {
	n := len(frames)
	if n > maxStackDepth {
		n = maxStackDepth
	}
	parts := make([]string, n)
	for i := 0; i < n; i++ {
		parts[i] = frames[n-1-i].name
	}
	return strings.Join(parts, " -> ")
}

func formatLocation(file string, line int64) string {
	short := shortenPath(file)
	if short == "" {
		return "-"
	}
	if line > 0 {
		return fmt.Sprintf("%s:%d", short, line)
	}
	return short
}

func shortenPath(path string) string {
	if path == "" {
		return ""
	}
	norm := strings.ReplaceAll(path, "\\", "/")
	parts := strings.Split(norm, "/")
	if len(parts) <= 3 {
		return norm
	}
	return strings.Join(parts[len(parts)-3:], "/")
}

func escapeMarkdownCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "`", "'")
	return s
}

func formatPct(part, total int64) string {
	if total <= 0 {
		return "0.0%"
	}
	return fmt.Sprintf("%.1f%%", float64(part)*100.0/float64(total))
}

func formatDurationNanos(ns int64) string {
	if ns <= 0 {
		return "0ms"
	}
	switch {
	case ns >= 1_000_000_000:
		return fmt.Sprintf("%.2fs", float64(ns)/1e9)
	case ns >= 1_000_000:
		return fmt.Sprintf("%.2fms", float64(ns)/1e6)
	case ns >= 1_000:
		return fmt.Sprintf("%.1fus", float64(ns)/1e3)
	default:
		return fmt.Sprintf("%dns", ns)
	}
}

func formatBytes(b int64) string {
	const (
		kib = 1024
		mib = 1024 * kib
		gib = 1024 * mib
	)
	switch {
	case b >= gib:
		return fmt.Sprintf("%.2f GiB", float64(b)/float64(gib))
	case b >= mib:
		return fmt.Sprintf("%.2f MiB", float64(b)/float64(mib))
	case b >= kib:
		return fmt.Sprintf("%.2f KiB", float64(b)/float64(kib))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
