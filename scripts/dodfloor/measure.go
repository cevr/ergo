package main

import (
	"bytes"
	"context"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/debug"
	"slices"
	"sort"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/experimental/irspike"
	linked "ergo.local/prototype/internal/linked/compiler"
	"ergo.local/prototype/internal/opcount"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketch/syntax"
)

type SourceTarget struct {
	Name  string
	Files []string
	Bytes int64
}

type WorkerTargetStat struct {
	Name         string  `json:"name"`
	Files        int     `json:"files"`
	Bytes        int64   `json:"bytes"`
	Instructions uint64  `json:"instructions"`
	Allocs       uint64  `json:"allocs"`
	AllocBytes   uint64  `json:"alloc_bytes"`
}

type WorkerResult struct {
	Mode              string             `json:"mode"`
	Units             int64              `json:"units"`
	TotalInstructions uint64             `json:"total_instructions"`
	Allocs            uint64             `json:"allocs"`
	AllocBytes        uint64             `json:"alloc_bytes"`
	NumGC             uint32             `json:"num_gc"`
	PeakRSSKiB        int64              `json:"peak_rss_kib"`
	Targets           []WorkerTargetStat `json:"targets,omitempty"`
}

type RoundStats struct {
	UnitLabel   string
	TotalUnits  int64
	OffInstrB   []float64
	GCInstrB    []float64
	AllocsB     []float64
	BytesB      []float64
	OffRSSKiB   []float64
	GCRSSKiB    []float64
	NumGCs      []uint32
}

type RollupSummary struct {
	Label       string
	Files       int
	Units       int64
	UnitName    string
	OffMed      float64
	OffSpread   float64
	GCMed       float64
	GCSpread    float64
	GCSharePct  float64
	AllocsPerB  float64
	BytesPerB   float64
	PeakRSSKiB  float64
}

type TargetRow struct {
	Name       string
	Files      int
	Bytes      int64
	OffInstrB  float64
	GCInstrB   float64
	GCSharePct float64
	AllocsPerB float64
	BytesPerB  float64
}

type MeasureReport struct {
	Rounds          int
	FormatSummary   RollupSummary
	FormatMedianTgt RollupSummary
	FormatBuckets   []RollupSummary
	FormatTargets   []TargetRow
	SyntaxSummary   RollupSummary
	SyntaxMedianTgt RollupSummary
	SyntaxBuckets   []RollupSummary
	IRSummary       RollupSummary
	Checkdump       RollupSummary
	IRDump          RollupSummary
	Nativebuild     RollupSummary
}

func discoverSourceTargets(root string) ([]SourceTarget, error) {
	collect := func(name, dir string) (SourceTarget, error) {
		t := SourceTarget{Name: name}
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" {
					return fs.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".ergo") {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			t.Files = append(t.Files, path)
			t.Bytes += info.Size()
			return nil
		})
		slices.Sort(t.Files)
		return t, err
	}

	var out []SourceTarget
	for _, fixed := range []struct {
		name string
		dir  string
	}{
		{"internal/sketch/std", filepath.Join(root, "internal/sketch/std")},
		{"compiler", filepath.Join(root, "compiler")},
	} {
		t, err := collect(fixed.name, fixed.dir)
		if err != nil {
			return nil, err
		}
		if len(t.Files) > 0 {
			out = append(out, t)
		}
	}

	exEntries, err := os.ReadDir(filepath.Join(root, "examples"))
	if err != nil {
		return nil, err
	}
	var exNames []string
	for _, e := range exEntries {
		if e.IsDir() && e.Name() != "testdata" {
			exNames = append(exNames, e.Name())
		}
	}
	slices.Sort(exNames)
	for _, name := range exNames {
		t, err := collect("examples/"+name, filepath.Join(root, "examples", name))
		if err != nil {
			return nil, err
		}
		if len(t.Files) > 0 {
			out = append(out, t)
		}
	}
	return out, nil
}

type targetPayloads struct {
	Name    string
	Sources []string
	Bytes   int64
}

type irWorkItem struct {
	Name     string
	Words    []uint32
	Strs     []string
	WantJSON string
}

type workerPayload struct {
	Mode        string
	VerifyExact bool
	Targets     []targetPayloads
	IRItems     []irWorkItem
}

func runWorker(payloadPath, outPath string) error {
	f, err := os.Open(payloadPath)
	if err != nil {
		return err
	}
	var pl workerPayload
	if err := gob.NewDecoder(f).Decode(&pl); err != nil {
		f.Close()
		return err
	}
	f.Close()

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	ctx := context.Background()
	res := WorkerResult{Mode: pl.Mode}

	switch pl.Mode {
	case "syntax", "format":
		isFormat := pl.Mode == "format"
		res.Targets = make([]WorkerTargetStat, len(pl.Targets))
		var totalAllocs, totalBytes uint64
		var totalInstr uint64
		var totalSrcBytes int64
		var mBefore, mAfter, startStats, endStats runtime.MemStats
		runtime.ReadMemStats(&startStats)
		for i, tgt := range pl.Targets {
			runtime.ReadMemStats(&mBefore)
			var errInner error
			nInstr, cerr := opcount.CountThread(func() {
				for _, src := range tgt.Sources {
					if isFormat {
						got, e := linked.FormatSource(ctx, src, 0)
						if e != nil && errInner == nil {
							errInner = e
						} else if !strings.HasPrefix(got, "ok\n") && errInner == nil {
							errInner = fmt.Errorf("format failed on %s: %.120q", tgt.Name, got)
						}
					} else {
						cols, e := linked.Columns(ctx, src)
						if e != nil && errInner == nil {
							errInner = e
						} else if len(cols) == 0 && errInner == nil {
							errInner = fmt.Errorf("empty columns on %s", tgt.Name)
						}
					}
				}
			})
			runtime.ReadMemStats(&mAfter)
			if cerr != nil {
				return cerr
			}
			if errInner != nil {
				return errInner
			}
			dAllocs := mAfter.Mallocs - mBefore.Mallocs
			dBytes := mAfter.TotalAlloc - mBefore.TotalAlloc
			res.Targets[i] = WorkerTargetStat{
				Name:         tgt.Name,
				Files:        len(tgt.Sources),
				Bytes:        tgt.Bytes,
				Instructions: nInstr,
				Allocs:       dAllocs,
				AllocBytes:   dBytes,
			}
			totalInstr += nInstr
			totalAllocs += dAllocs
			totalBytes += dBytes
			totalSrcBytes += tgt.Bytes
		}
		runtime.ReadMemStats(&endStats)
		res.Units = totalSrcBytes
		res.TotalInstructions = totalInstr
		res.Allocs = totalAllocs
		res.AllocBytes = totalBytes
		res.NumGC = endStats.NumGC - startStats.NumGC
	case "ir":
		var outBytes int64
		for _, it := range pl.IRItems {
			outBytes += int64(len(it.WantJSON))
		}
		var mBefore, mAfter runtime.MemStats
		runtime.ReadMemStats(&mBefore)
		var errInner error
		nInstr, cerr := opcount.CountThread(func() {
			for _, it := range pl.IRItems {
				got, e := linked.IRWrite(ctx, it.Words, it.Strs)
				if e != nil && errInner == nil {
					errInner = e
					return
				}
				text, ok := strings.CutPrefix(got, "ok\n")
				if !ok || (pl.VerifyExact && text != it.WantJSON) {
					if errInner == nil {
						at := 0
						for at < len(text) && at < len(it.WantJSON) && text[at] == it.WantJSON[at] {
							at++
						}
						from := max(0, at-60)
						errInner = fmt.Errorf("ir %s mismatch at %d (ok=%v gotLen=%d wantLen=%d)\ngot : %.180q\nwant: %.180q", it.Name, at, ok, len(text), len(it.WantJSON), text[from:], it.WantJSON[from:])
					}
					return
				}
			}
		})
		runtime.ReadMemStats(&mAfter)
		if cerr != nil {
			return cerr
		}
		if errInner != nil {
			return errInner
		}
		res.Units = outBytes
		res.TotalInstructions = nInstr
		res.Allocs = mAfter.Mallocs - mBefore.Mallocs
		res.AllocBytes = mAfter.TotalAlloc - mBefore.TotalAlloc
		res.NumGC = mAfter.NumGC - mBefore.NumGC
	default:
		return fmt.Errorf("unknown worker mode %q", pl.Mode)
	}

	res.PeakRSSKiB = readVmHWMKiB()
	data, err := json.Marshal(res)
	if err != nil {
		return err
	}
	return os.WriteFile(outPath, data, 0o644)
}

func readVmHWMKiB() int64 {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if rest, ok := strings.CutPrefix(line, "VmHWM:"); ok {
			fields := strings.Fields(rest)
			if len(fields) >= 1 {
				if v, err := strconv.ParseInt(fields[0], 10, 64); err == nil {
					return v
				}
			}
		}
	}
	return 0
}

func RunMeasureFast(root string, rounds int) (MeasureReport, error) {
	if rounds < 1 {
		rounds = 3
	}
	if err := opcount.Available(); err != nil {
		return MeasureReport{}, fmt.Errorf("opcount unavailable: %w", err)
	}
	tmp, err := os.MkdirTemp("", "dodfloor-*")
	if err != nil {
		return MeasureReport{}, err
	}
	defer os.RemoveAll(tmp)

	selfBin := filepath.Join(tmp, "dodfloor-self")
	buildSelf := exec.Command("go", "build", "-p", "2", "-o", selfBin, "./scripts/dodfloor")
	buildSelf.Dir = root
	if out, err := buildSelf.CombinedOutput(); err != nil {
		return MeasureReport{}, fmt.Errorf("build scripts/dodfloor: %v\n%s", err, out)
	}

	targets, err := discoverSourceTargets(root)
	if err != nil {
		return MeasureReport{}, err
	}
	payloads := make([]targetPayloads, len(targets))
	for i, t := range targets {
		srcs := make([]string, len(t.Files))
		for j, p := range t.Files {
			b, err := os.ReadFile(p)
			if err != nil {
				return MeasureReport{}, err
			}
			srcs[j] = string(b)
		}
		payloads[i] = targetPayloads{Name: t.Name, Sources: srcs, Bytes: t.Bytes}
	}
	fmtGob := filepath.Join(tmp, "fmt.gob")
	synGob := filepath.Join(tmp, "syn.gob")
	if err := writeGob(fmtGob, workerPayload{Mode: "format", Targets: payloads}); err != nil {
		return MeasureReport{}, err
	}
	if err := writeGob(synGob, workerPayload{Mode: "syntax", Targets: payloads}); err != nil {
		return MeasureReport{}, err
	}
	fmtOff, fmtGC, err := measureWorkerRounds(selfBin, fmtGob, rounds)
	if err != nil {
		return MeasureReport{}, err
	}
	synOff, synGC, err := measureWorkerRounds(selfBin, synGob, rounds)
	if err != nil {
		return MeasureReport{}, err
	}
	fmtSum, fmtMed, fmtBuckets, fmtRows := summarizeTargets("format", fmtOff, fmtGC)
	synSum, synMed, synBuckets, _ := summarizeTargets("syntax", synOff, synGC)
	empty := RollupSummary{Label: "(skipped)"}
	return MeasureReport{
		Rounds:          rounds,
		FormatSummary:   fmtSum,
		FormatMedianTgt: fmtMed,
		FormatBuckets:   fmtBuckets,
		FormatTargets:   fmtRows,
		SyntaxSummary:   synSum,
		SyntaxMedianTgt: synMed,
		SyntaxBuckets:   synBuckets,
		IRSummary:       empty,
		Checkdump:       empty,
		IRDump:          empty,
		Nativebuild:     empty,
	}, nil
}

func RunMeasure(root, ergoBin string, rounds int, verifyExact bool) (MeasureReport, error) {
	if rounds < 1 {
		rounds = 5
	}
	if err := opcount.Available(); err != nil {
		return MeasureReport{}, fmt.Errorf("opcount unavailable: %w", err)
	}
	tmp, err := os.MkdirTemp("", "dodfloor-*")
	if err != nil {
		return MeasureReport{}, err
	}
	defer os.RemoveAll(tmp)

	selfBin := filepath.Join(tmp, "dodfloor-self")
	buildSelf := exec.Command("go", "build", "-p", "2", "-o", selfBin, "./scripts/dodfloor")
	buildSelf.Dir = root
	if out, err := buildSelf.CombinedOutput(); err != nil {
		return MeasureReport{}, fmt.Errorf("build scripts/dodfloor: %v\n%s", err, out)
	}

	targets, err := discoverSourceTargets(root)
	if err != nil {
		return MeasureReport{}, err
	}
	payloads := make([]targetPayloads, len(targets))
	for i, t := range targets {
		srcs := make([]string, len(t.Files))
		for j, p := range t.Files {
			b, err := os.ReadFile(p)
			if err != nil {
				return MeasureReport{}, err
			}
			srcs[j] = string(b)
		}
		payloads[i] = targetPayloads{Name: t.Name, Sources: srcs, Bytes: t.Bytes}
	}

	fmtGob := filepath.Join(tmp, "fmt.gob")
	synGob := filepath.Join(tmp, "syn.gob")
	if err := writeGob(fmtGob, workerPayload{Mode: "format", Targets: payloads}); err != nil {
		return MeasureReport{}, err
	}
	if err := writeGob(synGob, workerPayload{Mode: "syntax", Targets: payloads}); err != nil {
		return MeasureReport{}, err
	}

	fmt.Fprintf(os.Stderr, "[dodfloor] building IR workload from corpus examples...\n")
	irGob := filepath.Join(tmp, "ir.gob")
	manifestPath := filepath.Join(tmp, "manifest")
	rootsPath := filepath.Join(tmp, "roots")
	var corpusBytes int64
	var corpusFiles int
	var irFileCount int
	prepCmd := exec.Command(selfBin, "-prep-ir", tmp, "-strict="+strconv.FormatBool(verifyExact))
	prepOut, err := prepCmd.CombinedOutput()
	if err != nil {
		return MeasureReport{}, fmt.Errorf("prep IR workload: %v\n%s", err, prepOut)
	}
	if _, serr := fmt.Sscanf(strings.TrimSpace(string(prepOut)), "%d %d %d", &irFileCount, &corpusFiles, &corpusBytes); serr != nil {
		return MeasureReport{}, fmt.Errorf("parse prep output %q: %w", string(prepOut), serr)
	}

	fmt.Fprintf(os.Stderr, "[dodfloor] measuring format (%d targets, %d rounds x2)...\n", len(targets), rounds)
	fmtOff, fmtGC, err := measureWorkerRounds(selfBin, fmtGob, rounds)
	if err != nil {
		return MeasureReport{}, fmt.Errorf("format rounds: %w", err)
	}
	fmt.Fprintf(os.Stderr, "[dodfloor] measuring syntax (%d targets, %d rounds x2)...\n", len(targets), rounds)
	synOff, synGC, err := measureWorkerRounds(selfBin, synGob, rounds)
	if err != nil {
		return MeasureReport{}, fmt.Errorf("syntax rounds: %w", err)
	}
	fmt.Fprintf(os.Stderr, "[dodfloor] measuring ir (%d files, %d rounds x2)...\n", irFileCount, rounds)
	irOff, irGC, err := measureWorkerRounds(selfBin, irGob, rounds)
	if err != nil {
		return MeasureReport{}, fmt.Errorf("ir rounds: %w", err)
	}

	fmtSum, fmtMed, fmtBuckets, fmtRows := summarizeTargets("format", fmtOff, fmtGC)
	synSum, synMed, synBuckets, _ := summarizeTargets("syntax", synOff, synGC)
	irSum := summarizeSimple("ir-write", "IR bytes", irOff, irGC)

	fmt.Fprintf(os.Stderr, "[dodfloor] building/measuring checkdump, irdump, nativebuild...\n")
	checkSum, irDumpSum, nativeSum, err := measureCompilerCLIs(root, ergoBin, tmp, rounds, manifestPath, rootsPath, corpusFiles, corpusBytes)
	if err != nil {
		return MeasureReport{}, err
	}

	return MeasureReport{
		Rounds:          rounds,
		FormatSummary:   fmtSum,
		FormatMedianTgt: fmtMed,
		FormatBuckets:   fmtBuckets,
		FormatTargets:   fmtRows,
		SyntaxSummary:   synSum,
		SyntaxMedianTgt: synMed,
		SyntaxBuckets:   synBuckets,
		IRSummary:       irSum,
		Checkdump:       checkSum,
		IRDump:          irDumpSum,
		Nativebuild:     nativeSum,
	}, nil
}

func writeGob(path string, v any) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return gob.NewEncoder(f).Encode(v)
}

func measureWorkerRounds(selfBin, gobPath string, rounds int) ([]WorkerResult, []WorkerResult, error) {
	off := make([]WorkerResult, 0, rounds)
	gc := make([]WorkerResult, 0, rounds)
	for r := range rounds {
		for _, gcEnv := range []string{"GOGC=off", "GOGC=100"} {
			outPath := gobPath + fmt.Sprintf(".%s.%d.json", gcEnv, r)
			cmd := exec.Command(selfBin, "-worker-payload", gobPath, "-worker-out", outPath)
			cmd.Env = append(os.Environ(), gcEnv)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			run, err := opcount.Count(cmd, 0)
			if err != nil {
				return nil, nil, fmt.Errorf("round %d (%s): %w\n%s", r, gcEnv, err, stderr.String())
			}
			if kind, why := run.Refusal(); kind != "" {
				return nil, nil, fmt.Errorf("round %d (%s) refused (%v): %s\n%s", r, gcEnv, kind, why, stderr.String())
			}
			data, err := os.ReadFile(outPath)
			if err != nil {
				return nil, nil, err
			}
			var res WorkerResult
			if err := json.Unmarshal(data, &res); err != nil {
				return nil, nil, err
			}
			if res.PeakRSSKiB == 0 {
				res.PeakRSSKiB = run.MaxRSSKiB
			}
			if gcEnv == "GOGC=off" {
				off = append(off, res)
			} else {
				gc = append(gc, res)
			}
		}
	}
	return off, gc, nil
}

func summarizeTargets(label string, off, gc []WorkerResult) (RollupSummary, RollupSummary, []RollupSummary, []TargetRow) {
	nT := len(off[0].Targets)
	files := 0
	for _, t := range off[0].Targets {
		files += t.Files
	}
	weighted := summarizeSimple(label+" (all "+strconv.Itoa(nT)+")", "source bytes", off, gc)
	weighted.Files = files

	tgtOff := make([][]float64, nT)
	tgtGC := make([][]float64, nT)
	tgtAllocs := make([][]float64, nT)
	tgtBytes := make([][]float64, nT)
	roundMedianOff := make([]float64, len(off))
	roundMedianGC := make([]float64, len(gc))

	for r := range off {
		var perOff, perGC []float64
		for i, t := range off[r].Targets {
			b := float64(t.Bytes)
			vOff := float64(t.Instructions) / b
			vGC := float64(gc[r].Targets[i].Instructions) / b
			tgtOff[i] = append(tgtOff[i], vOff)
			tgtGC[i] = append(tgtGC[i], vGC)
			tgtAllocs[i] = append(tgtAllocs[i], float64(t.Allocs)/b)
			tgtBytes[i] = append(tgtBytes[i], float64(t.AllocBytes)/b)
			perOff = append(perOff, vOff)
			perGC = append(perGC, vGC)
		}
		roundMedianOff[r] = opcount.Median(perOff)
		roundMedianGC[r] = opcount.Median(perGC)
	}

	rows := make([]TargetRow, nT)
	var medOffs, medGCs, medAllocs, medBytes []float64
	for i, t := range off[0].Targets {
		om := opcount.Median(tgtOff[i])
		gm := opcount.Median(tgtGC[i])
		gcShare := 0.0
		if gm > 0 {
			gcShare = ((gm - om) / gm) * 100
		}
		al := opcount.Median(tgtAllocs[i])
		by := opcount.Median(tgtBytes[i])
		rows[i] = TargetRow{
			Name:       t.Name,
			Files:      t.Files,
			Bytes:      t.Bytes,
			OffInstrB:  om,
			GCInstrB:   gm,
			GCSharePct: gcShare,
			AllocsPerB: al,
			BytesPerB:  by,
		}
		medOffs = append(medOffs, om)
		medGCs = append(medGCs, gm)
		medAllocs = append(medAllocs, al)
		medBytes = append(medBytes, by)
	}

	medSummary := RollupSummary{
		Label:      label + " (median target)",
		Files:      1,
		UnitName:   "source bytes",
		OffMed:     opcount.Median(roundMedianOff),
		OffSpread:  spreadPct(roundMedianOff),
		GCMed:      opcount.Median(roundMedianGC),
		GCSpread:   spreadPct(roundMedianGC),
		AllocsPerB: opcount.Median(medAllocs),
		BytesPerB:  opcount.Median(medBytes),
	}
	if medSummary.GCMed > 0 {
		medSummary.GCSharePct = ((medSummary.GCMed - medSummary.OffMed) / medSummary.GCMed) * 100
	}

	bucketFilter := func(name string, pred func(string) bool) RollupSummary {
		var bOff, bGC []WorkerResult
		for r := range off {
			var wo, wg WorkerResult
			for i, t := range off[r].Targets {
				if !pred(t.Name) {
					continue
				}
				wo.Units += t.Bytes
				wo.TotalInstructions += t.Instructions
				wo.Allocs += t.Allocs
				wo.AllocBytes += t.AllocBytes
				wg.Units += gc[r].Targets[i].Bytes
				wg.TotalInstructions += gc[r].Targets[i].Instructions
			}
			wo.PeakRSSKiB = off[r].PeakRSSKiB
			wg.PeakRSSKiB = gc[r].PeakRSSKiB
			bOff = append(bOff, wo)
			bGC = append(bGC, wg)
		}
		s := summarizeSimple(name, "source bytes", bOff, bGC)
		for _, t := range off[0].Targets {
			if pred(t.Name) {
				s.Files += t.Files
			}
		}
		return s
	}

	buckets := []RollupSummary{
		bucketFilter("internal/sketch/std", func(n string) bool { return n == "internal/sketch/std" }),
		bucketFilter("compiler", func(n string) bool { return n == "compiler" }),
		bucketFilter("examples", func(n string) bool { return strings.HasPrefix(n, "examples/") }),
	}

	return weighted, medSummary, buckets, rows
}

func summarizeSimple(label, unitName string, off, gc []WorkerResult) RollupSummary {
	offVals := make([]float64, len(off))
	gcVals := make([]float64, len(gc))
	allocs := make([]float64, len(off))
	bytes := make([]float64, len(off))
	rss := make([]float64, 0, len(off)+len(gc))
	for i := range off {
		u := float64(max(1, off[i].Units))
		offVals[i] = float64(off[i].TotalInstructions) / u
		gcVals[i] = float64(gc[i].TotalInstructions) / u
		allocs[i] = float64(off[i].Allocs) / u
		bytes[i] = float64(off[i].AllocBytes) / u
		rss = append(rss, float64(off[i].PeakRSSKiB), float64(gc[i].PeakRSSKiB))
	}
	om := opcount.Median(offVals)
	gm := opcount.Median(gcVals)
	gcPct := 0.0
	if gm > 0 {
		gcPct = ((gm - om) / gm) * 100
	}
	return RollupSummary{
		Label:      label,
		Units:      off[0].Units,
		UnitName:   unitName,
		OffMed:     om,
		OffSpread:  spreadPct(offVals),
		GCMed:      gm,
		GCSpread:   spreadPct(gcVals),
		GCSharePct: gcPct,
		AllocsPerB: opcount.Median(allocs),
		BytesPerB:  opcount.Median(bytes),
		PeakRSSKiB: opcount.Median(rss),
	}
}

func spreadPct(xs []float64) float64 {
	if len(xs) <= 1 {
		return 0
	}
	med := opcount.Median(xs)
	if med == 0 {
		return 0
	}
	lo, hi := xs[0], xs[0]
	for _, x := range xs[1:] {
		if x < lo {
			lo = x
		}
		if x > hi {
			hi = x
		}
	}
	return ((hi - lo) / med) * 100
}

func runPrepIR(root, tmp string, verifyExact bool) error {
	irItems, loadedUnits, err := collectIRWorkload(root)
	if err != nil {
		return err
	}
	irGob := filepath.Join(tmp, "ir.gob")
	if err := writeGob(irGob, workerPayload{Mode: "ir", VerifyExact: verifyExact, IRItems: irItems}); err != nil {
		return err
	}
	_, _, corpusBytes, corpusFiles, err := buildPureCorpusManifest(root, tmp, loadedUnits)
	if err != nil {
		return err
	}
	fmt.Printf("%d %d %d\n", len(irItems), corpusFiles, corpusBytes)
	return nil
}

func measureCompilerCLIs(root, ergoBin, tmp string, rounds int, manifest, rootsFile string, corpusFiles int, corpusBytes int64) (RollupSummary, RollupSummary, RollupSummary, error) {
	if ergoBin == "" {
		cand := filepath.Join(root, "bin/ergo")
		if _, err := os.Stat(cand); err == nil {
			ergoBin = cand
		} else {
			ergoBin = filepath.Join(tmp, "ergo")
			buildErgo := exec.Command("go", "build", "-p", "2", "-o", ergoBin, "./cmd/ergo")
			buildErgo.Dir = root
			if out, err := buildErgo.CombinedOutput(); err != nil {
				return RollupSummary{}, RollupSummary{}, RollupSummary{}, fmt.Errorf("go build ./cmd/ergo: %v\n%s", err, out)
			}
		}
	}
	if abs, err := filepath.Abs(ergoBin); err == nil {
		ergoBin = abs
	}

	buildCmd := func(pkg, name string) (string, error) {
		bin := filepath.Join(tmp, name)
		cmd := exec.Command(ergoBin, "build", "-o", bin, pkg)
		cmd.Dir = filepath.Join(root, "compiler")
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("ergo build %s: %v\n%s", pkg, err, out)
		}
		return bin, nil
	}

	checkdumpBin, err := buildCmd("./cmd/checkdump", "checkdump")
	if err != nil {
		return RollupSummary{}, RollupSummary{}, RollupSummary{}, err
	}
	irdumpBin, err := buildCmd("./cmd/irdump", "irdump")
	if err != nil {
		return RollupSummary{}, RollupSummary{}, RollupSummary{}, err
	}
	nativebuildBin, err := buildCmd("./cmd/nativebuild", "nativebuild")
	if err != nil {
		return RollupSummary{}, RollupSummary{}, RollupSummary{}, err
	}

	stdDir := filepath.Join(root, "internal/sketch/std")

	checkSum, err := runCLIRounds("checkdump", corpusFiles, corpusBytes, "source bytes", rounds, func(gcEnv string) *exec.Cmd {
		c := exec.Command(checkdumpBin, "-std", stdDir, "-threads", "1", "-corpus", manifest)
		c.Env = append(os.Environ(), gcEnv)
		return c
	})
	if err != nil {
		return RollupSummary{}, RollupSummary{}, RollupSummary{}, fmt.Errorf("checkdump: %w", err)
	}

	irDumpSum, err := runCLIRounds("irdump", corpusFiles, corpusBytes, "source bytes", rounds, func(gcEnv string) *exec.Cmd {
		c := exec.Command(irdumpBin, "-std", stdDir, "-roots", rootsFile, "-corpus", manifest)
		c.Env = append(os.Environ(), gcEnv)
		return c
	})
	if err != nil {
		return RollupSummary{}, RollupSummary{}, RollupSummary{}, fmt.Errorf("irdump: %w", err)
	}

	rtCmd := exec.Command("go", "list", "-export", "-f", "{{.Export}}", "runtime")
	rtCmd.Dir = root
	rtOut, err := rtCmd.CombinedOutput()
	if err != nil {
		return RollupSummary{}, RollupSummary{}, RollupSummary{}, fmt.Errorf("go list runtime: %v\n%s", err, rtOut)
	}
	runtimeA := strings.TrimSpace(string(rtOut))
	intmathFile := filepath.Join(root, "compiler/cmd/nativebuild/testdata/intmath/intmath.ergo")
	intmathInfo, err := os.Stat(intmathFile)
	if err != nil {
		return RollupSummary{}, RollupSummary{}, RollupSummary{}, err
	}
	outA := filepath.Join(tmp, "intmath.a")
	nativeSum, err := runCLIRounds("nativebuild", 1, intmathInfo.Size(), "source bytes", rounds, func(gcEnv string) *exec.Cmd {
		c := exec.Command(nativebuildBin, "build", "-std", stdDir, "-p", "testdata/intmath", "-header", runtimeA, "-o", outA, intmathFile)
		c.Env = append(os.Environ(), gcEnv)
		return c
	})
	if err != nil {
		return RollupSummary{}, RollupSummary{}, RollupSummary{}, fmt.Errorf("nativebuild: %w", err)
	}

	return checkSum, irDumpSum, nativeSum, nil
}

func runCLIRounds(label string, files int, units int64, unitName string, rounds int, makeCmd func(gcEnv string) *exec.Cmd) (RollupSummary, error) {
	offVals := make([]float64, 0, rounds)
	gcVals := make([]float64, 0, rounds)
	rssVals := make([]float64, 0, 2*rounds)
	u := float64(max(1, units))
	for r := range rounds {
		for _, gcEnv := range []string{"GOGC=off", "GOGC=100"} {
			cmd := makeCmd(gcEnv)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			run, err := opcount.Count(cmd, 0)
			if err != nil {
				return RollupSummary{}, fmt.Errorf("round %d (%s): %w\n%s", r, gcEnv, err, stderr.String())
			}
			if kind, why := run.Refusal(); kind != "" {
				return RollupSummary{}, fmt.Errorf("round %d (%s) refused (%v): %s\n%s", r, gcEnv, kind, why, stderr.String())
			}
			val := float64(run.Instructions) / u
			rssVals = append(rssVals, float64(run.MaxRSSKiB))
			if gcEnv == "GOGC=off" {
				offVals = append(offVals, val)
			} else {
				gcVals = append(gcVals, val)
			}
		}
	}
	om := opcount.Median(offVals)
	gm := opcount.Median(gcVals)
	gcPct := 0.0
	if gm > 0 {
		gcPct = ((gm - om) / gm) * 100
	}
	return RollupSummary{
		Label:      label,
		Files:      files,
		Units:      units,
		UnitName:   unitName,
		OffMed:     om,
		OffSpread:  spreadPct(offVals),
		GCMed:      gm,
		GCSpread:   spreadPct(gcVals),
		GCSharePct: gcPct,
		PeakRSSKiB: opcount.Median(rssVals),
	}, nil
}

func buildPureCorpusManifest(root, tmp string, loadedUnits []lower.Unit) (manifestPath, rootsPath string, totalBytes int64, totalFiles int, err error) {
	emptyExt := filepath.Join(tmp, "empty.ext")
	emptyForeign := filepath.Join(tmp, "empty.foreign")
	if err := os.WriteFile(emptyExt, nil, 0o644); err != nil {
		return "", "", 0, 0, err
	}
	if err := os.WriteFile(emptyForeign, nil, 0o644); err != nil {
		return "", "", 0, 0, err
	}

	candidates := []string{
		"examples/counter",
		"examples/editions/sketch",
		"compiler/cmd/nativebuild/testdata/intmath",
	}
	var pureUnits []lower.Unit
	seen := map[string]bool{}
	var addUnit func(u lower.Unit)
	addUnit = func(u lower.Unit) {
		for _, imp := range u.Imports {
			addUnit(imp)
		}
		if u.Package == nil || seen[u.Package.Path] {
			return
		}
		seen[u.Package.Path] = true
		pureUnits = append(pureUnits, u)
	}
	var rootPaths []string
	for _, u := range loadedUnits {
		if hasGoDep(u) {
			continue
		}
		addUnit(u)
		if u.Package != nil {
			rootPaths = append(rootPaths, u.Package.Path)
		}
	}

	var lines []string
	for _, u := range pureUnits {
		var files []string
		for _, src := range u.Sources {
			if src.Path == "" {
				continue
			}
			files = append(files, src.Path)
			if info, serr := os.Stat(src.Path); serr == nil {
				totalBytes += info.Size()
				totalFiles++
			}
		}
		if len(files) > 0 {
			lines = append(lines, strings.Join(append([]string{u.Package.Path, emptyExt, emptyForeign}, files...), " "))
		}
	}
	for _, rel := range candidates {
		dir := filepath.Join(root, filepath.FromSlash(rel))
		entries, derr := os.ReadDir(dir)
		if derr != nil {
			continue
		}
		var files []string
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".ergo") {
				p := filepath.Join(dir, e.Name())
				files = append(files, p)
				if info, serr := os.Stat(p); serr == nil {
					totalBytes += info.Size()
					totalFiles++
				}
			}
		}
		if len(files) > 0 {
			sort.Strings(files)
			pkgPath := rel
			if !seen[pkgPath] {
				lines = append(lines, strings.Join(append([]string{pkgPath, emptyExt, emptyForeign}, files...), " "))
				rootPaths = append(rootPaths, pkgPath)
			}
		}
	}
	manifestPath = filepath.Join(tmp, "manifest")
	rootsPath = filepath.Join(tmp, "roots")
	if err := os.WriteFile(manifestPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		return "", "", 0, 0, err
	}
	if err := os.WriteFile(rootsPath, []byte(strings.Join(rootPaths, "\n")+"\n"), 0o644); err != nil {
		return "", "", 0, 0, err
	}
	return manifestPath, rootsPath, totalBytes, totalFiles, nil
}

func hasGoDep(u lower.Unit) bool {
	if len(u.Go.Modules) > 0 {
		return true
	}
	for _, f := range u.Files {
		for _, d := range f.Decls {
			id, ok := d.(*syntax.ImportDecl)
			if !ok {
				continue
			}
			for _, sp := range id.Specs {
				if sp.Kind == syntax.ImportGo {
					return true
				}
			}
		}
	}
	for _, imp := range u.Imports {
		if hasGoDep(imp) {
			return true
		}
	}
	return false
}

func collectIRWorkload(root string) ([]irWorkItem, []lower.Unit, error) {
	ctx := context.Background()
	kindsText, err := linked.IRKinds(ctx)
	if err != nil {
		return nil, nil, err
	}
	ks, err := parseIRKindTable(kindsText)
	if err != nil {
		return nil, nil, err
	}
	preferred := []string{
		"examples/calc/cmd/calc",
		"examples/jsonparse/cmd/jsonparse",
		"examples/kvstore/cmd/kvstore",
		"examples/lru/cmd/lru",
		"examples/regex/cmd/regex",
		"examples/pipeline/cmd/pipeline",
		"examples/sudoku/cmd/sudoku",
		"examples/tasks-cli/cmd/tasks-cli",
		"examples/jobq/cmd/jobq",
		"examples/flattree/cmd/flattree",
		"examples/logstat/cmd/logstat",
		"examples/mdhtml/cmd/mdhtml",
		"examples/ssg/cmd/ssg",
		"examples/twig/cmd/twig",
		"examples/router/cmd/router",
		"examples/cron/cmd/cron",
	}
	var cmdDirs []string
	for _, rel := range preferred {
		d := filepath.Join(root, filepath.FromSlash(rel))
		if fi, serr := os.Stat(d); serr == nil && fi.IsDir() {
			cmdDirs = append(cmdDirs, d)
		}
	}
	if len(cmdDirs) == 0 {
		mods, gerr := filepath.Glob(filepath.Join(root, "examples", "*", "ergo.mod"))
		if gerr != nil {
			return nil, nil, gerr
		}
		sort.Strings(mods)
		for _, m := range mods {
			cmds, _ := filepath.Glob(filepath.Join(filepath.Dir(m), "cmd", "*"))
			sort.Strings(cmds)
			cmdDirs = append(cmdDirs, cmds...)
			if len(cmdDirs) >= 16 {
				cmdDirs = cmdDirs[:16]
				break
			}
		}
	}
	var items []irWorkItem
	var units []lower.Unit
	for _, d := range cmdDirs {
		u, lerr := irspike.Load(d)
		if lerr != nil {
			continue
		}
		units = append(units, u)
		out, _, _, lerr := lower.LowerIRSplit(u, false)
		if lerr != nil {
			continue
		}
		for _, f := range out {
			if filepath.Base(f.Path) != lower.IRFileName {
				continue
			}
			var decoded irFileMirror
			dec := json.NewDecoder(bytes.NewReader(f.Data))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&decoded); err != nil {
				return nil, nil, fmt.Errorf("%s/%s: %w", d, f.Path, err)
			}
			again, err := json.Marshal(&decoded)
			if err != nil {
				return nil, nil, err
			}
			packed, err := packIR(ks, &decoded)
			if err != nil {
				return nil, nil, err
			}
			items = append(items, irWorkItem{
				Name:     filepath.Base(d) + ":" + f.Path,
				Words:    packed.words,
				Strs:     packed.strs,
				WantJSON: string(again),
			})
		}
	}
	if len(items) == 0 {
		return nil, nil, fmt.Errorf("no IR corpus items found")
	}
	_ = debug.SetGCPercent
	return items, units, nil
}

type irFileMirror struct {
	V      int          `json:"v"`
	Files  []string     `json:"files"`
	Funcs  []irFuncMirror `json:"funcs"`
	Shared []string     `json:"shared,omitempty"`
}

type irFuncMirror struct {
	Name   string          `json:"name"`
	Pos    irPosMirror     `json:"pos"`
	End    irPosMirror     `json:"end"`
	Locals []irLocalMirror `json:"locals,omitempty"`
	Body   []*irStmtMirror `json:"body"`
}

type irLocalMirror struct {
	Name string      `json:"name"`
	Type string      `json:"type"`
	Pos  irPosMirror `json:"pos"`
}

type irPosMirror [3]int

type irStmtMirror struct {
	K       string          `json:"k"`
	Pos     irPosMirror     `json:"pos"`
	Local   int             `json:"local,omitempty"`
	Key     *int            `json:"key,omitempty"`
	Val     *int            `json:"val,omitempty"`
	X       *irExprMirror   `json:"x,omitempty"`
	Then    []*irStmtMirror `json:"then,omitempty"`
	Else    []*irStmtMirror `json:"else,omitempty"`
	Body    []*irStmtMirror `json:"body,omitempty"`
	End     irPosMirror     `json:"end"`
	ElsePos irPosMirror     `json:"epos"`
	ElseEnd irPosMirror     `json:"eend"`
	Op      string          `json:"op,omitempty"`
	Cases   []*irCaseMirror `json:"cases,omitempty"`
	Label   string          `json:"label,omitempty"`
	Y       *irExprMirror   `json:"y,omitempty"`
	Ls      []int           `json:"ls,omitempty"`
}

type irExprMirror struct {
	K      string          `json:"k"`
	Pos    irPosMirror     `json:"pos"`
	I      int             `json:"i,omitempty"`
	T      string          `json:"t,omitempty"`
	V      string          `json:"v,omitempty"`
	Op     string          `json:"op,omitempty"`
	X      *irExprMirror   `json:"x,omitempty"`
	Y      *irExprMirror   `json:"y,omitempty"`
	Name   string          `json:"name,omitempty"`
	Args   []*irExprMirror `json:"args,omitempty"`
	Elems  []*irExprMirror `json:"elems,omitempty"`
	Keys   []string        `json:"keys,omitempty"`
	Params []int           `json:"params,omitempty"`
	Body   []*irStmtMirror `json:"body,omitempty"`
	End    *irPosMirror    `json:"end,omitempty"`
}

type irCaseMirror struct {
	Pos   irPosMirror     `json:"pos"`
	Types []string        `json:"types"`
	Body  []*irStmtMirror `json:"body,omitempty"`
	Vals  []*irExprMirror `json:"vals,omitempty"`
	Def   bool            `json:"def,omitempty"`
}

type irKindDef struct {
	code, class uint32
	name        string
	slots       []int
}

type irKindMap struct {
	byKey map[string]irKindDef
}

func parseIRKindTable(text string) (irKindMap, error) {
	m := irKindMap{byKey: map[string]irKindDef{}}
	for line := range strings.Lines(text) {
		head, slots, ok := strings.Cut(strings.TrimSuffix(line, "\n"), "|")
		f := strings.SplitN(head, " ", 3)
		if !ok || len(f) != 3 {
			return m, fmt.Errorf("bad kind line %q", line)
		}
		code, _ := strconv.Atoi(f[0])
		class, _ := strconv.Atoi(f[1])
		k := irKindDef{code: uint32(code), class: uint32(class), name: f[2]}
		if slots != "" {
			for s := range strings.SplitSeq(slots, ",") {
				n, _ := strconv.Atoi(s)
				k.slots = append(k.slots, n)
			}
		}
		m.byKey[strconv.Itoa(int(k.class))+" "+k.name] = k
	}
	return m, nil
}

type packedIR struct {
	words []uint32
	strs  []string
}

const (
	opText = 1 + iota
	opWord
	opPos
	opList
	opRow
	opFunc
	opLocal
	opEnd
	opFile
	opShared
	opNoFiles
	opNoFuncs
)

type irPackCtx struct {
	kinds irKindMap
	out   packedIR
	ids   map[string]uint32
}

func (p *irPackCtx) str(s string) uint32 {
	if id, ok := p.ids[s]; ok {
		return id
	}
	id := uint32(len(p.out.strs))
	p.out.strs = append(p.out.strs, s)
	p.ids[s] = id
	return id
}

func (p *irPackCtx) emit(ws ...uint32) { p.out.words = append(p.out.words, ws...) }

func packPos(x irPosMirror) []uint32 { return []uint32{uint32(x[0]), uint32(x[1]), uint32(x[2])} }

func packIR(kinds irKindMap, f *irFileMirror) (packedIR, error) {
	p := &irPackCtx{kinds: kinds, out: packedIR{strs: []string{""}}, ids: map[string]uint32{"": 0}}
	p.emit(3, uint32(uint64(f.V)), uint32(uint64(f.V)>>32))
	for _, path := range f.Files {
		p.emit(opFile, p.str(path))
	}
	if f.Files != nil && len(f.Files) == 0 {
		p.emit(opNoFiles)
	}
	for _, s := range f.Shared {
		p.emit(opShared, p.str(s))
	}
	if f.Funcs != nil && len(f.Funcs) == 0 {
		p.emit(opNoFuncs)
	}
	for i := range f.Funcs {
		fn := &f.Funcs[i]
		p.emit(opFunc, p.str(fn.Name))
		p.emit(packPos(fn.Pos)...)
		p.emit(packPos(fn.End)...)
		for _, l := range fn.Locals {
			p.emit(opLocal, p.str(l.Name), p.str(l.Type))
			p.emit(packPos(l.Pos)...)
		}
		if err := p.list(reflect.ValueOf(fn.Body)); err != nil {
			return p.out, err
		}
		p.emit(opEnd)
	}
	return p.out, nil
}

func (p *irPackCtx) list(v reflect.Value) error {
	if v.IsNil() {
		p.emit(opWord, 0)
		return nil
	}
	for i := range v.Len() {
		if v.Index(i).IsNil() {
			p.emit(opWord, 0)
			continue
		}
		if err := p.row(v.Index(i)); err != nil {
			return err
		}
	}
	p.emit(opList, uint32(v.Len()))
	return nil
}

func (p *irPackCtx) row(v reflect.Value) error {
	e := v.Elem()
	class, first, name := uint32(6), 1, ""
	switch e.Interface().(type) {
	case irStmtMirror:
		class, first, name = 4, 2, e.Field(0).String()
	case irExprMirror:
		class, first, name = 5, 2, e.Field(0).String()
	}
	k, ok := p.kinds.byKey[strconv.Itoa(int(class))+" "+name]
	if !ok {
		return fmt.Errorf("missing IR kind %d %q", class, name)
	}
	for _, s := range k.slots {
		if err := p.field(e.Field(s)); err != nil {
			return err
		}
	}
	p.emit(opRow, k.code)
	p.emit(packPos(e.Field(first - 1).Interface().(irPosMirror))...)
	return nil
}

func (p *irPackCtx) field(v reflect.Value) error {
	switch x := v.Interface().(type) {
	case string:
		p.emit(opText, p.str(x))
	case int:
		p.emit(opWord, uint32(int32(x)))
	case *int:
		if x == nil {
			p.emit(opWord, 0)
		} else {
			p.emit(opWord, uint32(*x)+1)
		}
	case bool:
		if x {
			p.emit(opWord, 1)
		} else {
			p.emit(opWord, 0)
		}
	case irPosMirror:
		if x == (irPosMirror{}) {
			p.emit(opWord, 0)
		} else {
			p.emit(opPos)
			p.emit(packPos(x)...)
		}
	case *irPosMirror:
		if x == nil {
			p.emit(opWord, 0)
		} else {
			p.emit(opPos)
			p.emit(packPos(*x)...)
		}
	case *irExprMirror:
		if x == nil {
			p.emit(opWord, 0)
		} else {
			return p.row(v)
		}
	case []string:
		if x == nil {
			p.emit(opWord, 0)
			return nil
		}
		for _, s := range x {
			p.emit(opText, p.str(s))
		}
		p.emit(opList, uint32(len(x)))
	case []int:
		if x == nil {
			p.emit(opWord, 0)
			return nil
		}
		for _, n := range x {
			p.emit(opWord, uint32(int32(n)))
		}
		p.emit(opList, uint32(len(x)))
	case []*irStmtMirror, []*irExprMirror, []*irCaseMirror:
		return p.list(v)
	default:
		return fmt.Errorf("unsupported slot %s", v.Type())
	}
	return nil
}

func unusedFloorHelper() {
	_ = math.MaxInt32
}
