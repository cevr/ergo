package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"time"
)

// receipt is the text of docs/receipts/std-count.md.
func receipt(repo string, cs []Case, results []map[string]perOp, uses []int, rounds int, loadBefore, loadAfter string) string {
	var b strings.Builder
	head := gitOut(repo, "rev-parse", "HEAD")
	dirty := gitOut(repo, "status", "--porcelain", "--", "runtime", "internal/sketch", "cmd/ergo", "scripts/stdcount")
	state := "clean"
	if dirty != "" {
		state = "with uncommitted changes in runtime, internal/sketch, cmd/ergo or scripts/stdcount"
	}
	b.WriteString("# Std count: Ergo's std against Go's, in counted instructions and allocations\n\n")
	b.WriteString("Written by `go run ./scripts/stdcount` (std-better SB0). Do not edit by hand: run the harness again.\n\n")
	fmt.Fprintf(&b, "Measured head: `%s` (%s), %s.\n\n", head, state, time.Now().UTC().Format("2006-01-02"))
	b.WriteString("## Method\n\n")
	b.WriteString("- Each case is one std function. Its **Ergo** form is the std call in an Ergo program, as a user writes it, built by `ergo build` from this checkout; its **Go** form is the Go std function in a plain Go program; its **hand** form, where there is one, is the Go a careful programmer writes instead (the plan names it).\n")
	b.WriteString("- Each form runs the operation n times over 64 seeded inputs (input i mod 64) and folds each result into a checksum it prints. Every form of a case prints the same checksum at n = 1000 before any count is taken: Go is the oracle of the result.\n")
	b.WriteString("- **Instructions**: user-space instructions retired by the process and its threads (`perf_event_open`, hardware instructions, kernel excluded, inherited, enabled at exec). GC and runtime work on other threads counts. Kernel work does not: the write system calls per operation (`syscw` of `/proc/self/io`) stand in for it.\n")
	b.WriteString("- **Allocations**: `runtime.MemStats` `Mallocs` and `TotalAlloc`, printed at exit by a hook (`internal/runtime/exithook`) added to both programs' modules.\n")
	fmt.Fprintf(&b, "- **Per operation** = (count at N − count at N/4) / (3N/4): start-up and input set-up cancel. The median of %d interleaved rounds; *spread* is (max − min) / median of the Ergo form's instruction slopes. `GOMAXPROCS=1`, `GODEBUG=asyncpreemptoff=1`.\n", rounds)
	b.WriteString("- **Uses** is the number of times the case's call text occurs in `examples/**/*.ergo`: a static weight, not a run-time count.\n")
	b.WriteString("- Counts are a receipt, not a time: they hold under host load. No test asserts a count.\n\n")
	fmt.Fprintf(&b, "Host: %s, %s, %s/%s, load average %s at the start and %s at the end.\n\n", cpuModel(), goVersion(), runtime.GOOS, runtime.GOARCH, loadBefore, loadAfter)

	b.WriteString("## Counts\n\n")
	b.WriteString("Per operation. *Ergo/Go* is the instruction ratio; above 1.00 the Ergo form does more work than Go's.\n\n")
	b.WriteString("| Package | Case | Uses | Ergo instr | Go instr | Ergo/Go | Hand instr | Ergo allocs (B) | Go allocs (B) | Hand allocs (B) | Spread | Note |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for i, c := range cs {
		e, g := results[i]["ergo"], results[i]["go"]
		hand, handAllocs := "—", "—"
		if h, ok := results[i]["hand"]; ok {
			hand, handAllocs = fmtInstr(h.instr), fmtAllocs(h.sample)
		}
		fmt.Fprintf(&b, "| `%s` | %s | %d | %s | %s | %s | %s | %s | %s | %s | %.1f %% | %s |\n",
			c.Pkg, c.Name, uses[i], fmtInstr(e.instr), fmtInstr(g.instr), ratio(e.instr, g.instr), hand,
			fmtAllocs(e.sample), fmtAllocs(g.sample), handAllocs, 100*e.spread, orDash(c.Note))
	}

	b.WriteString("\n## Weighted\n\n")
	b.WriteString("Extra instructions per operation (Ergo − Go) times the uses, largest first: where a better std body or a general fact pays most across the examples. A negative value is a case where the Ergo form already does less work.\n\n")
	b.WriteString("| Package | Case | Uses | Ergo − Go instr | Weighted |\n| --- | --- | --- | --- | --- |\n")
	order := make([]int, len(cs))
	for i := range order {
		order[i] = i
	}
	weight := func(i int) float64 { return (results[i]["ergo"].instr - results[i]["go"].instr) * float64(uses[i]) }
	slices.SortStableFunc(order, func(a, b int) int {
		wa, wb := weight(a), weight(b)
		switch {
		case wa > wb:
			return -1
		case wa < wb:
			return 1
		}
		return 0
	})
	for _, i := range order {
		fmt.Fprintf(&b, "| `%s` | %s | %d | %.0f | %.0f |\n", cs[i].Pkg, cs[i].Name, uses[i], results[i]["ergo"].instr-results[i]["go"].instr, weight(i))
	}

	b.WriteString("\n## Cells\n\n")
	b.WriteString("The text each measured package's \"Lowering against hand Go\" cell of `docs/spec/stdlib.md` (Std strengths over Effect) carries after `counted:`, from this run.\n\n")
	var pkgs []string
	for _, c := range cs {
		if !slices.Contains(pkgs, c.Pkg) {
			pkgs = append(pkgs, c.Pkg)
		}
	}
	for _, p := range pkgs {
		var parts []string
		for i, c := range cs {
			if c.Pkg != p {
				continue
			}
			e, g := results[i]["ergo"], results[i]["go"]
			part := fmt.Sprintf("%s %s of Go's instructions, %s against %s allocations", c.Name, ratio(e.instr, g.instr), trim(e.mallocs), trim(g.mallocs))
			if h, ok := results[i]["hand"]; ok {
				part += fmt.Sprintf(" (hand Go %s instructions, %s allocations)", ratio(h.instr, g.instr), trim(h.mallocs))
			}
			parts = append(parts, part)
		}
		fmt.Fprintf(&b, "- `%s`: %s\n", p, strings.Join(parts, "; "))
	}
	return b.String()
}

func fmtInstr(x float64) string {
	if x >= 100 {
		return fmt.Sprintf("%.0f", x)
	}
	return fmt.Sprintf("%.1f", x)
}

func trim(x float64) string {
	switch {
	case x < 0.005 && x > -0.005:
		return "0"
	case x == float64(int(x)):
		return fmt.Sprintf("%d", int(x))
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", x), "0"), ".")
}

func fmtAllocs(s sample) string {
	bytes := trim(s.bytes)
	if s.bytes >= 10 {
		bytes = fmt.Sprintf("%.0f", s.bytes)
	}
	text := fmt.Sprintf("%s (%s)", trim(s.mallocs), bytes)
	if s.writes > 0.005 {
		text += fmt.Sprintf(", %s writes", trim(s.writes))
	}
	return text
}

func ratio(a, b float64) string {
	if b <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.2fx", a/b)
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func gitOut(repo string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func cpuModel() string {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return "unknown CPU"
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "model name"); ok {
			return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), ":"))
		}
	}
	return "unknown CPU"
}

func goVersion() string {
	out, err := exec.Command("go", "version").Output()
	if err != nil {
		return runtime.Version()
	}
	return strings.TrimSpace(string(out))
}
