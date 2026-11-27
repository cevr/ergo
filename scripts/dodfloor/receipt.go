package main

import (
	"fmt"
	"strings"
)

func RenderMarkdownReceipt(sha string, staticRep StaticReport, baseline *MeasureReport, measured MeasureReport) string {
	var b strings.Builder
	b.WriteString("# Compiler DoD Floor Receipt\n\n")
	if sha != "" {
		fmt.Fprintf(&b, "- Commit: `%s`\n", sha)
	}
	fmt.Fprintf(&b, "- Measurement method: `internal/opcount` (`CountThread` in-process per-target + `Count` isolated child processes, `%d` rounds alternating `GOGC=off` and `GOGC=100`, median and spread `(max-min)/median`, peak RSS from `VmHWM` / `ru_maxrss`).\n", measured.Rounds)
	fmt.Fprintf(&b, "- Reference baseline (`docs/receipts/self-host.md` at `e56dafb19`): weighted `fmt 191.5`, `parse 69.5` instr/byte across 98 targets (6,285,816 bytes); median target `fmt 234.6`, `parse 83.8`; `compiler` `fmt 265.39`, `parse 85.70`, `GC 10.8%%`, `0.078 allocs/B`, `37.38 bytes/B`.\n\n")

	b.WriteString("## Static DoD Invariants (`compiler/**`)\n\n")
	fmt.Fprintf(&b, "- Files scanned: `%d`\n", staticRep.FilesScanned)
	fmt.Fprintf(&b, "- Structs scanned: `%d`, fields scanned: `%d`, `Map` types scanned: `%d`, functions scanned (`syntax`/`format`/`ir`): `%d`\n",
		staticRep.StructsScanned, staticRep.FieldsScanned, staticRep.MapTypesScanned, staticRep.FuncsScanned)
	if len(staticRep.Findings) == 0 {
		b.WriteString("- Findings: **0 violations** (`no-pointer-keyed-maps`: PASS, `dense-uint32-ids`: PASS, `no-per-node-scratch-allocs`: PASS)\n\n")
	} else {
		fmt.Fprintf(&b, "- Findings: **%d violations**\n\n", len(staticRep.Findings))
		for _, f := range staticRep.Findings {
			fmt.Fprintf(&b, "  - `%s:%d:%d` [`%s`] %s\n", f.File, f.Line, f.Col, f.Rule, f.Message)
		}
		b.WriteString("\n")
	}

	b.WriteString("## Corpus DoD Summary\n\n")
	if baseline != nil {
		b.WriteString("| Workload | Units | Base `GOGC=off` | New `GOGC=off` | Delta | Base `GOGC=100` | New `GOGC=100` | Base GC% | New GC% | Base `allocs/B` | New `allocs/B` | Base `bytes/B` | New `bytes/B` | New Spread (`off`) | Peak RSS (KiB) |\n")
		b.WriteString("| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |\n")
	if len(baseline.FormatBuckets) == 3 && len(measured.FormatBuckets) == 3 &&
		len(baseline.SyntaxBuckets) == 3 && len(measured.SyntaxBuckets) == 3 {
		for _, p := range [][2]RollupSummary{
			{baseline.FormatSummary, measured.FormatSummary},
			{baseline.FormatMedianTgt, measured.FormatMedianTgt},
			{baseline.FormatBuckets[0], measured.FormatBuckets[0]},
			{baseline.FormatBuckets[1], measured.FormatBuckets[1]},
			{baseline.FormatBuckets[2], measured.FormatBuckets[2]},
			{baseline.SyntaxSummary, measured.SyntaxSummary},
			{baseline.SyntaxMedianTgt, measured.SyntaxMedianTgt},
			{baseline.SyntaxBuckets[0], measured.SyntaxBuckets[0]},
			{baseline.SyntaxBuckets[1], measured.SyntaxBuckets[1]},
			{baseline.SyntaxBuckets[2], measured.SyntaxBuckets[2]},
			{baseline.IRSummary, measured.IRSummary},
			{baseline.Checkdump, measured.Checkdump},
			{baseline.IRDump, measured.IRDump},
			{baseline.Nativebuild, measured.Nativebuild},
		} {
			writeDeltaRow(&b, p[0], p[1])
		}
	} else {
		pairs := [][2]RollupSummary{
			{baseline.FormatSummary, measured.FormatSummary},
			{baseline.FormatMedianTgt, measured.FormatMedianTgt},
			{baseline.SyntaxSummary, measured.SyntaxSummary},
			{baseline.SyntaxMedianTgt, measured.SyntaxMedianTgt},
		}
		for _, p := range pairs {
			writeDeltaRow(&b, p[0], p[1])
		}
	}
	} else {
		b.WriteString("| Workload | Files | Units | `GOGC=off` instr/unit | `off` spread | `GOGC=100` instr/unit | `100` spread | GC% | `allocs/B` | `bytes/B` | Peak RSS (KiB) |\n")
		b.WriteString("| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |\n")
		rows := []RollupSummary{
			measured.FormatSummary,
			measured.FormatMedianTgt,
			measured.FormatBuckets[0],
			measured.FormatBuckets[1],
			measured.FormatBuckets[2],
			measured.SyntaxSummary,
			measured.SyntaxMedianTgt,
			measured.SyntaxBuckets[0],
			measured.SyntaxBuckets[1],
			measured.SyntaxBuckets[2],
			measured.IRSummary,
			measured.Checkdump,
			measured.IRDump,
			measured.Nativebuild,
		}
		for _, r := range rows {
			writeSingleRow(&b, r)
		}
	}
	b.WriteString("\n")

	if len(measured.FormatTargets) > 0 {
		b.WriteString("## Format Targets (Per Target)\n\n")
		b.WriteString("```\n")
		b.WriteString(fmt.Sprintf("%-35s %5s %9s %9s %9s %7s %9s %9s\n", "target", "files", "bytes", "fmt", "fmt-gc", "gc%", "allocs/B", "bytes/B"))
		for _, r := range measured.FormatTargets {
			b.WriteString(fmt.Sprintf("%-35s %5d %9d %9.2f %9.2f %6.1f%% %9.4f %9.2f\n",
				r.Name, r.Files, r.Bytes, r.OffInstrB, r.GCInstrB, r.GCSharePct, r.AllocsPerB, r.BytesPerB))
		}
		b.WriteString("```\n")
	}
	return b.String()
}

func writeSingleRow(b *strings.Builder, r RollupSummary) {
	allocStr, bytesStr := "-", "-"
	if r.AllocValid() {
		allocStr = fmt.Sprintf("%.4f", r.AllocsPerB)
		bytesStr = fmt.Sprintf("%.2f", r.BytesPerB)
	}
	rssStr := "-"
	if r.PeakRSSKiB > 0 {
		rssStr = fmt.Sprintf("%.0f", r.PeakRSSKiB)
	}
	fmt.Fprintf(b, "| %s | %d | %d | %.2f | %.2f%% | %.2f | %.2f%% | %.1f%% | %s | %s | %s |\n",
		r.Label, r.Files, r.Units, r.OffMed, r.OffSpread, r.GCMed, r.GCSpread, r.GCSharePct, allocStr, bytesStr, rssStr)
}

func writeDeltaRow(b *strings.Builder, base, cur RollupSummary) {
	deltaPct := 0.0
	if base.OffMed > 0 {
		deltaPct = ((cur.OffMed - base.OffMed) / base.OffMed) * 100
	}
	baseAl, curAl, baseBy, curBy := "-", "-", "-", "-"
	if base.AllocValid() {
		baseAl = fmt.Sprintf("%.4f", base.AllocsPerB)
		baseBy = fmt.Sprintf("%.2f", base.BytesPerB)
	}
	if cur.AllocValid() {
		curAl = fmt.Sprintf("%.4f", cur.AllocsPerB)
		curBy = fmt.Sprintf("%.2f", cur.BytesPerB)
	}
	rssStr := "-"
	if cur.PeakRSSKiB > 0 {
		rssStr = fmt.Sprintf("%.0f", cur.PeakRSSKiB)
	}
	fmt.Fprintf(b, "| %s | %d | %.2f | %.2f | %+.2f%% | %.2f | %.2f | %.1f%% | %.1f%% | %s | %s | %s | %s | %.2f%% | %s |\n",
		cur.Label, cur.Units, base.OffMed, cur.OffMed, deltaPct, base.GCMed, cur.GCMed, base.GCSharePct, cur.GCSharePct, baseAl, curAl, baseBy, curBy, cur.OffSpread, rssStr)
}

func (r RollupSummary) AllocValid() bool {
	return r.AllocsPerB > 0 || r.BytesPerB > 0
}
