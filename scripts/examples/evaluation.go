package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"ergo.local/prototype/internal/report"
)

// checkTarget uses the same package and default seed as the native test
// leg. A skipped or bounded obligation is visible, never called agreement.
func (r *runner) checkTarget(p *program, target, leg string) *report.Evaluation {
	o, err := r.tool(r.cfg.timeout, "check", target)
	if err != nil || !o.passed() {
		r.toolFailed(p, leg, o, err)
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(o.stdout))
	e, why := checkedEvaluation(dec)
	if why == "" && dec.More() {
		why = "no valid evaluation envelope"
	}
	if why != "" {
		fmt.Fprintf(r.out, "FAIL %s: %s: %s\n", p.name, leg, why)
		r.fail(p.name + ": " + leg)
		return nil
	}
	return e
}

// checkModule runs one `ergo check DIR/...` for a program of packages and
// returns each package's evaluation, by its import path (TEST-one): the
// command prints one envelope per package, in the order its test run
// printed the packages (order). It returns nil when the run or any of its
// envelopes is not what checkTarget accepts; the caller then checks each
// package alone, which reports it as before.
func (r *runner) checkModule(p *program, order []string) map[string]*report.Evaluation {
	o, err := r.tool(r.cfg.timeout*time.Duration(len(order)), "check", filepath.Join(p.dir, "..."))
	if err != nil || o.overCap() != "" || o.timedOut || !o.passed() {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(o.stdout))
	out := map[string]*report.Evaluation{}
	for _, pkg := range order {
		e, why := checkedEvaluation(dec)
		if why != "" {
			return nil
		}
		out[pkg] = e
	}
	if dec.More() {
		return nil
	}
	return out
}

// checkedEvaluation decodes the next check envelope of dec and returns its
// evaluation, or why it is not a valid one.
func checkedEvaluation(dec *json.Decoder) (*report.Evaluation, string) {
	var envelope struct {
		Checked    bool               `json:"checked"`
		Evaluation *report.Evaluation `json:"evaluation"`
	}
	if err := dec.Decode(&envelope); err != nil || !envelope.Checked || envelope.Evaluation == nil || envelope.Evaluation.SchemaVersion != report.EvaluationSchemaVersion {
		return nil, "no valid evaluation envelope"
	}
	var statuses []string
	for _, o := range envelope.Evaluation.Obligations {
		statuses = append(statuses, o.Status)
	}
	if envelope.Evaluation.Status != report.EvaluationStatus(statuses) {
		return nil, "inconsistent evaluation summary"
	}
	return envelope.Evaluation, ""
}

type nativeEvaluation struct {
	status, evidence string
	cases            int
}

var nativeCases = regexp.MustCompile(`^\((?:proved: (?:all )?)?(\d+) cases?\)`)

// nativeEvaluations reads the native runner's verdict lines, not its
// summary (exit 0 alone would also accept missing or skipped obligations).
func nativeEvaluations(stdout []byte) map[string]nativeEvaluation {
	out := map[string]nativeEvaluation{}
	for _, line := range strings.Split(string(stdout), "\n") {
		var v nativeEvaluation
		switch {
		case strings.HasPrefix(line, "ok "):
			line, v.status, v.evidence, v.cases = strings.TrimPrefix(line, "ok "), "passed", "evaluated", 1
		case strings.HasPrefix(line, "FAIL ") && !strings.HasPrefix(line, "FAIL ("):
			line, v.status = strings.TrimPrefix(line, "FAIL "), "refuted"
		case strings.HasPrefix(line, "skip "):
			line, v.status = strings.TrimPrefix(line, "skip "), "skipped"
		default:
			continue
		}
		name, details, _ := strings.Cut(line, " (")
		name, _, _ = strings.Cut(name, ":")
		if details != "" && v.status == "passed" {
			v.evidence = "tested"
			if strings.HasPrefix(details, "proved:") || strings.HasPrefix(details, "proved by ") {
				v.evidence = "proved"
			}
			if m := nativeCases.FindStringSubmatch("(" + details); m != nil {
				v.cases, _ = strconv.Atoi(m[1])
			}
		}
		out[name] = v
	}
	return out
}

func (r *runner) compareEvaluation(p *program, leg string, e *report.Evaluation, stdout []byte) {
	native := nativeEvaluations(stdout)
	seen := map[string]bool{}
	fail := func(name, why string) {
		fmt.Fprintf(r.out, "FAIL %s: %s: %s: %s\n", p.name, leg, name, why)
		r.fail(p.name + ": " + leg)
	}
	for _, o := range e.Obligations {
		seen[o.Name] = true
		switch o.Status {
		case "skipped", "incomplete", "not run":
			fmt.Fprintf(r.out, "skip %s: %s: %s: %s (%s); native test still runs\n", p.name, leg, o.Name, o.Status, o.Reason)
		case "passed":
			n, ok := native[o.Name]
			if !ok || n.status != "passed" || n.evidence != o.Evidence || (o.Machine == nil && n.cases != o.Cases) {
				fail(o.Name, fmt.Sprintf("check %s/%s (%d cases), native %s/%s (%d cases; present %t)", o.Status, o.Evidence, o.Cases, n.status, n.evidence, n.cases, ok))
			} else {
				fmt.Fprintf(r.out, "ok %s: %s: %s agrees (%s)\n", p.name, leg, o.Name, o.Evidence)
			}
		default:
			fail(o.Name, "check "+o.Status+": "+o.Reason)
		}
	}
	for name := range native {
		if !seen[name] {
			fail(name, "native obligation missing from check")
		}
	}
	if len(e.Obligations) == 0 && len(native) == 0 {
		fmt.Fprintf(r.out, "ok %s: %s (no obligations)\n", p.name, leg)
	}
}
