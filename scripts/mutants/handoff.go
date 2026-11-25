package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"ergo.local/prototype/scripts/mutants/anchor"
)

type nativeMutantVerdict struct {
	Name       string   `json:"name"`
	File       string   `json:"file"`
	Decl       string   `json:"decl,omitempty"`
	Status     string   `json:"status"`
	Killer     string   `json:"killer,omitempty"`
	Runner     string   `json:"runner,omitempty"`
	Cached     bool     `json:"cached,omitempty"`
	Detail     string   `json:"detail,omitempty"`
	ReachedBy  []string `json:"reachedBy,omitempty"`
	External   []string `json:"externalKillers,omitempty"`
	DurationMs int64    `json:"durationMs"`
}

type nativeMutantReport struct {
	Ledgers  []string              `json:"ledgers"`
	Killed   int                   `json:"killed"`
	Cached   int                   `json:"cached"`
	Survived int                   `json:"survived"`
	Invalid  int                   `json:"invalid"`
	Unjudged int                   `json:"unjudged"`
	Gap      int                   `json:"gap"`
	Skipped  int                   `json:"skipped"`
	Results  []nativeMutantVerdict `json:"results"`
}

// resolveErgoBinary finds an ergo binary for delegating .ergo mutant rows:
// explicit -ergo flag, then <root>/bin/ergo, then "ergo" on PATH.
// Returns "" when disabled or when no executable binary is available.
func resolveErgoBinary(root, flagPath string, disabled bool) string {
	if disabled {
		return ""
	}
	if flagPath != "" {
		if abs, err := filepath.Abs(flagPath); err == nil {
			return abs
		}
		return flagPath
	}
	if absRoot, err := filepath.Abs(root); err == nil {
		cand := filepath.Join(absRoot, "bin", "ergo")
		if isExecutableFile(cand) {
			return cand
		}
	}
	if found, err := exec.LookPath("ergo"); err == nil {
		return found
	}
	return ""
}

func isExecutableFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0
}

// partitionDelegatedJobs splits jobs into rows that ergo test --mutants can
// judge natively and rows that stay on scripts/mutants's Go worker path.
func partitionDelegatedJobs(root string, cfg Config, jobs []Job) (delegated, remaining []Job) {
	if cfg.All {
		return nil, jobs
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, jobs
	}
	for _, j := range jobs {
		var delMutants, remMutants []*Mutant
		for _, m := range j.Ledger.Mutants {
			if j.Select != nil && !j.Select.MatchString(m.Name) {
				continue
			}
			if canDelegateRow(absRoot, j, m) {
				delMutants = append(delMutants, m)
			} else {
				remMutants = append(remMutants, m)
			}
		}
		if len(delMutants) > 0 {
			delegated = append(delegated, Job{
				Path: j.Path,
				Ledger: &Ledger{
					About:   j.Ledger.About,
					Mutants: delMutants,
				},
			})
		}
		if len(remMutants) > 0 {
			remaining = append(remaining, Job{
				Path: j.Path,
				Ledger: &Ledger{
					About:    j.Ledger.About,
					Env:      j.Ledger.Env,
					Commands: j.Ledger.Commands,
					Mutants:  remMutants,
				},
				Select: j.Select,
			})
		}
	}
	return delegated, remaining
}

func canDelegateRow(absRoot string, j Job, m *Mutant) bool {
	if !strings.HasSuffix(m.File, ".ergo") {
		return false
	}
	if m.Regenerate != nil || len(j.Ledger.Env) > 0 {
		return false
	}
	ids := killerIDs(m)
	if len(ids) == 0 {
		if m.Gap != "" {
			return false
		}
		ledgerDirRel := ""
		if absLedger, err := filepath.Abs(j.Path); err == nil {
			if rel, err := filepath.Rel(absRoot, filepath.Dir(filepath.Dir(absLedger))); err == nil && !strings.HasPrefix(rel, "..") {
				ledgerDirRel = filepath.ToSlash(rel)
			}
		}
		if ledgerDirRel != "" && goFiles(absRoot, ledgerDirRel, true) {
			return false
		}
		return ergoPackage(absRoot, filepath.ToSlash(filepath.Dir(m.File)))
	}
	for _, id := range ids {
		if !isNativeErgoKiller(absRoot, id) {
			return false
		}
	}
	return true
}

func isNativeErgoKiller(absRoot, id string) bool {
	suite, name := splitID(id)
	if suite == "" || name == "" || suite == commandSuite {
		return false
	}
	if suite == "examples" {
		if mod, ok := strings.CutPrefix(name, "modules/"); ok {
			dir := filepath.Join(absRoot, "modules", filepath.FromSlash(mod))
			info, err := os.Stat(dir)
			return err == nil && info.IsDir()
		}
		casesDir := filepath.Join(absRoot, "examples", filepath.FromSlash(name), "cases")
		info, err := os.Stat(casesDir)
		return err == nil && info.IsDir()
	}
	if strings.Contains(suite, "[") {
		return false
	}
	return ergoPackage(absRoot, suite)
}

func runDelegatedErgoJobs(ctx context.Context, ergoBin, root string, fresh bool, jobs []Job, log io.Writer) ([]Result, []Job, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, jobs, err
	}
	var allResults []Result
	var fallback []Job

	for _, j := range jobs {
		if len(j.Ledger.Mutants) == 0 {
			continue
		}
		byName := make(map[string]*Mutant, len(j.Ledger.Mutants))
		parts := make([]string, 0, len(j.Ledger.Mutants))
		for _, m := range j.Ledger.Mutants {
			byName[m.Name] = m
			parts = append(parts, regexp.QuoteMeta(m.Name))
		}
		pattern := "^(" + strings.Join(parts, "|") + ")$"
		absLedger, err := filepath.Abs(j.Path)
		if err != nil {
			return nil, nil, err
		}
		ledgerDir := filepath.Dir(filepath.Dir(absLedger))

		args := []string{"test", "--mutants", "--json", "--run", pattern}
		if fresh {
			args = append(args, "--fresh")
		}
		args = append(args, ledgerDir)

		if log != nil {
			fmt.Fprintf(log, "handoff: delegating %d .ergo row(s) of %s to %s test --mutants\n", len(j.Ledger.Mutants), j.Path, filepath.Base(ergoBin))
		}
		cmd := exec.CommandContext(ctx, ergoBin, args...)
		cmd.Dir = absRoot
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		runErr := cmd.Run()

		var rep nativeMutantReport
		if decErr := json.Unmarshal(stdout.Bytes(), &rep); decErr != nil {
			return nil, nil, fmt.Errorf("delegated ergo test --mutants (%s): %v (err=%v, stderr=%s)", j.Path, decErr, runErr, strings.TrimSpace(stderr.String()))
		}

		seen := map[string]bool{}
		var missing []*Mutant
		for _, v := range rep.Results {
			m, ok := byName[v.Name]
			if !ok {
				continue
			}
			if v.Status == "skipped" {
				missing = append(missing, m)
				seen[v.Name] = true
				continue
			}
			r := convertNativeVerdict(absRoot, j.Path, m, v)
			allResults = append(allResults, r)
			seen[v.Name] = true
			if log != nil {
				fmt.Fprintf(log, "%s: %s %s %s (%.0f s, %d run, %d stopped, %d covering)\n",
					r.Name, r.Status, r.Killer, r.Reason, r.Seconds, len(r.Ran), len(r.Stopped), r.Covering)
			}
		}
		for _, m := range j.Ledger.Mutants {
			if !seen[m.Name] {
				missing = append(missing, m)
			}
		}
		if len(missing) > 0 {
			fallback = append(fallback, Job{
				Path: j.Path,
				Ledger: &Ledger{
					About:   j.Ledger.About,
					Mutants: missing,
				},
			})
		}
	}
	return allResults, fallback, nil
}

func convertNativeVerdict(absRoot, ledgerPath string, m *Mutant, v nativeMutantVerdict) Result {
	line, locateErr := locateMutantLine(absRoot, m)
	r := Result{
		Ledger:  ledgerPath,
		Name:    m.Name,
		Line:    line,
		Seconds: float64(v.DurationMs) / 1000.0,
		Detail:  v.Detail,
	}
	known := killerIDs(m)
	switch v.Status {
	case "killed":
		killer := normalizeDelegatedKiller(v.Killer, known)
		r.Status = Killed
		r.Killer = killer
		r.Reason = "fail"
		r.Killers = []string{killer}
		r.Ran = []string{killer}
		r.Covering = max(1, len(known))
	case "survived", "gap":
		if len(v.ReachedBy) == 0 && len(known) == 0 {
			r.Status = Uncovered
		} else {
			r.Status = Survived
		}
		r.Ran = slices.Clone(v.ReachedBy)
		r.Covering = max(len(v.ReachedBy), len(known))
	case "invalid":
		if locateErr != nil {
			r.Status = Stale
		} else {
			r.Status = Invalid
		}
	case "unjudged":
		if strings.Contains(strings.ToLower(v.Detail), "timed out") {
			r.Status = Hung
		} else {
			r.Status = Unjudged
		}
	default:
		r.Status = Unjudged
	}
	return r
}

func locateMutantLine(absRoot string, m *Mutant) (int, error) {
	data, err := os.ReadFile(filepath.Join(absRoot, filepath.FromSlash(m.File)))
	if err != nil {
		return 0, err
	}
	at, err := anchor.Locate(m.File, data, m.Decl, m.From)
	if err != nil {
		return 0, err
	}
	return bytes.Count(data[:at], []byte{'\n'}) + 1, nil
}

func normalizeDelegatedKiller(raw string, known []string) string {
	if slices.Contains(known, raw) {
		return raw
	}
	// Map `pkg:law Name for Impl` back to canonical `pkg:law:Name`.
	suite, rest, ok := strings.Cut(raw, ":")
	if ok {
		if afterLaw, isLaw := strings.CutPrefix(rest, "law "); isLaw {
			lawName, _, _ := strings.Cut(afterLaw, " for ")
			cand := suite + ":law:" + strings.TrimSpace(lawName)
			if slices.Contains(known, cand) {
				return cand
			}
			for _, k := range known {
				ks, kn := splitID(k)
				if ks == suite {
					if kl, ok := strings.CutPrefix(kn, "law:"); ok && (kl == lawName || strings.HasSuffix(lawName, "."+kl) || strings.HasSuffix(kl, "."+lawName)) {
						return k
					}
				}
			}
			return cand
		}
	}
	return raw
}
