package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const defaultExemptionsRel = "internal/selfhostcheck/testdata/drift-exemptions.json"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("compiler-drift", flag.ContinueOnError)
	flags.SetOutput(stderr)

	var root string
	var checkDrift bool
	var checkExemptions bool
	var baseRev string
	var exemptionsPath string
	var groupStopsPath string
	var diffStops bool
	var failOnRegression bool
	var topN int
	var jsonOut bool

	flags.StringVar(&root, "root", ".", "repository checkout root")
	flags.BoolVar(&checkDrift, "check-drift", false, "check Go compiler rule edits against self-hosted compiler/ mirrors and drift exemptions")
	flags.BoolVar(&checkExemptions, "check-exemptions", false, "validate drift-exemptions.json (existing files, non-empty reason/owner, not expired)")
	flags.StringVar(&baseRev, "base", "", "git base `REV` for -check-drift")
	flags.StringVar(&exemptionsPath, "exemptions", "", "path to drift-exemptions.json (default: <root>/internal/selfhostcheck/testdata/drift-exemptions.json)")
	flags.StringVar(&groupStopsPath, "group-stops", "", "group and rank IR stop lines from `FILE` by structural fingerprint")
	flags.BoolVar(&diffStops, "diff-stops", false, "compare two IR stop logs (`<base> <head>`) and report reduced/regressed fingerprints and diffs")
	flags.BoolVar(&failOnRegression, "fail-on-regression", false, "with -diff-stops, exit 1 if any stop fingerprint grew or any new D diff appeared")
	flags.IntVar(&topN, "top", 20, "maximum fingerprints to display in -group-stops / -diff-stops (<=0 for all)")
	flags.BoolVar(&jsonOut, "json", false, "emit machine-readable JSON output")

	flags.Usage = func() {
		fmt.Fprintln(stderr, "usage:")
		fmt.Fprintln(stderr, "  go run ./scripts/compiler-drift -check-drift -base REV [-exemptions FILE] [-json]")
		fmt.Fprintln(stderr, "  go run ./scripts/compiler-drift -check-exemptions [-exemptions FILE] [-json]")
		fmt.Fprintln(stderr, "  go run ./scripts/compiler-drift -group-stops FILE [-top N] [-json]")
		fmt.Fprintln(stderr, "  go run ./scripts/compiler-drift -diff-stops [-fail-on-regression] [-top N] [-json] BASE HEAD")
		flags.PrintDefaults()
	}

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	modes := 0
	if checkDrift {
		modes++
	}
	if checkExemptions {
		modes++
	}
	if groupStopsPath != "" {
		modes++
	}
	if diffStops {
		modes++
	}
	if modes == 0 {
		flags.Usage()
		return 2
	}
	if modes > 1 && !(checkDrift && checkExemptions && groupStopsPath == "" && !diffStops) {
		fmt.Fprintln(stderr, "compiler-drift: choose one mode (-check-drift, -check-exemptions, -group-stops, or -diff-stops)")
		return 2
	}

	if exemptionsPath == "" {
		exemptionsPath = filepath.Join(root, filepath.FromSlash(defaultExemptionsRel))
	}

	now := time.Now()

	if checkExemptions && !checkDrift {
		return runCheckExemptions(root, exemptionsPath, now, jsonOut, stdout, stderr)
	}
	if checkDrift {
		if baseRev == "" {
			fmt.Fprintln(stderr, "compiler-drift: -check-drift requires -base REV")
			return 2
		}
		if checkExemptions {
			if code := runCheckExemptions(root, exemptionsPath, now, jsonOut, stdout, stderr); code != 0 {
				return code
			}
		}
		return runCheckDrift(root, baseRev, exemptionsPath, now, jsonOut, stdout, stderr)
	}
	if groupStopsPath != "" {
		return runGroupStops(groupStopsPath, topN, jsonOut, stdout, stderr)
	}
	if flags.NArg() != 2 {
		fmt.Fprintln(stderr, "compiler-drift: -diff-stops requires exactly 2 arguments: BASE HEAD")
		return 2
	}
	return runDiffStops(flags.Arg(0), flags.Arg(1), topN, failOnRegression, jsonOut, stdout, stderr)
}

func runCheckExemptions(root, exemptionsPath string, now time.Time, jsonOut bool, stdout, stderr io.Writer) int {
	reg, err := loadExemptions(exemptionsPath)
	if err != nil {
		fmt.Fprintf(stderr, "compiler-drift: %v\n", err)
		return 2
	}
	errs := validateExemptions(root, reg, now)
	if jsonOut {
		payload := struct {
			File       string   `json:"file"`
			Exemptions int      `json:"exemptions"`
			Valid      bool     `json:"valid"`
			Errors     []string `json:"errors"`
		}{
			File:       exemptionsPath,
			Exemptions: len(reg.Exemptions),
			Valid:      len(errs) == 0,
			Errors:     errs,
		}
		if err := writeJSON(stdout, payload); err != nil {
			fmt.Fprintf(stderr, "compiler-drift: %v\n", err)
			return 2
		}
	} else {
		for _, e := range errs {
			fmt.Fprintf(stdout, "exemption error: %s\n", e)
		}
		fmt.Fprintf(stdout, "drift-exemptions: %d entries, %d errors\n", len(reg.Exemptions), len(errs))
	}
	if len(errs) > 0 {
		return 1
	}
	return 0
}

func runCheckDrift(root, baseRev, exemptionsPath string, now time.Time, jsonOut bool, stdout, stderr io.Writer) int {
	reg, err := loadExemptions(exemptionsPath)
	if err != nil {
		fmt.Fprintf(stderr, "compiler-drift: %v\n", err)
		return 2
	}
	changed, err := gitChangedFiles(root, baseRev)
	if err != nil {
		fmt.Fprintf(stderr, "compiler-drift: %v\n", err)
		return 2
	}
	report := evaluateDrift(root, changed, reg, now)
	if jsonOut {
		if err := writeJSON(stdout, report); err != nil {
			fmt.Fprintf(stderr, "compiler-drift: %v\n", err)
			return 2
		}
	} else {
		printDriftReport(stdout, baseRev, report)
	}
	if len(report.Violations) > 0 {
		return 1
	}
	return 0
}

func printDriftReport(w io.Writer, baseRev string, r DriftReport) {
	if len(r.Violations) == 0 {
		fmt.Fprintf(w, "compiler-drift: PASS (0 unmirrored Go compiler rule edits since %s; %d mirrored, %d exempted)\n",
			baseRev, r.MirroredFiles, r.ExemptedFiles)
		return
	}
	fmt.Fprintf(w, "compiler-drift: FAIL (%d unmirrored Go compiler rule files since %s):\n", len(r.Violations), baseRev)
	for _, v := range r.Violations {
		note := ""
		if v.Note != "" {
			note = " (" + v.Note + ")"
		}
		fmt.Fprintf(w, "  [%s] %s -> mirror in %s or add exemption in %s%s\n",
			v.Subsystem, v.File, v.MirrorDir, defaultExemptionsRel, note)
	}
}

func runGroupStops(path string, topN int, jsonOut bool, stdout, stderr io.Writer) int {
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(stderr, "compiler-drift: %v\n", err)
		return 2
	}
	defer f.Close()
	log, err := ParseStopLog(f)
	if err != nil {
		fmt.Fprintf(stderr, "compiler-drift: %v\n", err)
		return 2
	}
	groups := log.RankedGroups(topN)
	if jsonOut {
		payload := struct {
			File               string      `json:"file"`
			TotalEvents        int         `json:"totalEvents"`
			DistinctFunctions  int         `json:"distinctFunctions"`
			DistinctFiles      int         `json:"distinctFiles"`
			UniqueFingerprints int         `json:"uniqueFingerprints"`
			DiffEvents         int         `json:"diffEvents"`
			Groups             []StopGroup `json:"groups"`
		}{
			File:               path,
			TotalEvents:        log.TotalEvents,
			DistinctFunctions:  log.DistinctFunctions,
			DistinctFiles:      log.DistinctFiles,
			UniqueFingerprints: len(log.Groups),
			DiffEvents:         len(log.Diffs),
			Groups:             groups,
		}
		if err := writeJSON(stdout, payload); err != nil {
			fmt.Fprintf(stderr, "compiler-drift: %v\n", err)
			return 2
		}
		return 0
	}
	fmt.Fprintf(stdout, "stops: %d events, %d distinct functions, %d distinct files, %d fingerprints, %d D diffs\n",
		log.TotalEvents, log.DistinctFunctions, log.DistinctFiles, len(log.Groups), len(log.Diffs))
	fmt.Fprintf(stdout, "%-6s %-7s %-6s %-10s %s\n", "FUNCS", "EVENTS", "FILES", "SUBSYS", "FINGERPRINT")
	for _, g := range groups {
		fmt.Fprintf(stdout, "%-6d %-7d %-6d %-10s %s\n",
			g.Functions, g.Events, g.Files, g.Fingerprint.Subsystem, g.Fingerprint.String())
	}
	return 0
}

func runDiffStops(basePath, headPath string, topN int, failOnRegression, jsonOut bool, stdout, stderr io.Writer) int {
	baseLog, err := parseStopFile(basePath)
	if err != nil {
		fmt.Fprintf(stderr, "compiler-drift: %v\n", err)
		return 2
	}
	headLog, err := parseStopFile(headPath)
	if err != nil {
		fmt.Fprintf(stderr, "compiler-drift: %v\n", err)
		return 2
	}
	diff := DiffStopLogs(baseLog, headLog)
	if jsonOut {
		out := diff
		if topN > 0 {
			if len(out.Reduced) > topN {
				out.Reduced = out.Reduced[:topN]
			}
			if len(out.Regressed) > topN {
				out.Regressed = out.Regressed[:topN]
			}
		}
		if err := writeJSON(stdout, out); err != nil {
			fmt.Fprintf(stderr, "compiler-drift: %v\n", err)
			return 2
		}
	} else {
		printStopDiff(stdout, diff, topN)
	}
	if failOnRegression && (len(diff.Regressed) > 0 || len(diff.NewDiffs) > 0) {
		return 1
	}
	return 0
}

func parseStopFile(p string) (*StopLog, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseStopLog(f)
}

func printStopDiff(w io.Writer, d StopDiff, topN int) {
	fmt.Fprintf(w, "stop-diff: functions %d -> %d (%+d), events %d -> %d (%+d), D diffs %d -> %d (%d new)\n",
		d.BaseFunctions, d.HeadFunctions, d.HeadFunctions-d.BaseFunctions,
		d.BaseEvents, d.HeadEvents, d.HeadEvents-d.BaseEvents,
		d.BaseDiffs, d.HeadDiffs, len(d.NewDiffs))
	limit := func(n int) int {
		if topN > 0 && n > topN {
			return topN
		}
		return n
	}
	if len(d.Reduced) > 0 {
		fmt.Fprintf(w, "\nREDUCED / ELIMINATED (%d fingerprints):\n", len(d.Reduced))
		for _, r := range d.Reduced[:limit(len(d.Reduced))] {
			fmt.Fprintf(w, "  %4d -> %4d (%+d funcs, %+d events) [%s] %s\n",
				r.BaseFunctions, r.HeadFunctions, r.DeltaFunctions, r.DeltaEvents,
				r.Fingerprint.Subsystem, r.Fingerprint.String())
		}
	}
	if len(d.Regressed) > 0 {
		fmt.Fprintf(w, "\nREGRESSED / NEW (%d fingerprints):\n", len(d.Regressed))
		for _, r := range d.Regressed[:limit(len(d.Regressed))] {
			fmt.Fprintf(w, "  %4d -> %4d (%+d funcs, %+d events) [%s] %s\n",
				r.BaseFunctions, r.HeadFunctions, r.DeltaFunctions, r.DeltaEvents,
				r.Fingerprint.Subsystem, r.Fingerprint.String())
		}
	}
	if len(d.NewDiffs) > 0 {
		fmt.Fprintf(w, "\nNEW IR DIFFS (%d):\n", len(d.NewDiffs))
		for _, nd := range d.NewDiffs[:limit(len(d.NewDiffs))] {
			fmt.Fprintf(w, "  D %s %s %s\n", nd.File, nd.Offset, strings.TrimSpace(nd.Reason))
		}
	}
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
