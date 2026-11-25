package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type ExemptionRegistry struct {
	About      string           `json:"about"`
	Exemptions []DriftExemption `json:"exemptions"`
}

type DriftExemption struct {
	File    string `json:"file"`
	Reason  string `json:"reason"`
	Owner   string `json:"owner"`
	Expires string `json:"expires,omitempty"`
}

type DriftViolation struct {
	Subsystem string `json:"subsystem"`
	File      string `json:"file"`
	MirrorDir string `json:"mirrorDir"`
	Note      string `json:"note,omitempty"`
}

type DriftReport struct {
	MirroredFiles int              `json:"mirroredFiles"`
	ExemptedFiles int              `json:"exemptedFiles"`
	Violations    []DriftViolation `json:"violations"`
}

var compilerSubsystems = []string{"check", "lower", "syntax", "format"}

func loadExemptions(p string) (*ExemptionRegistry, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("read exemptions %s: %w", p, err)
	}
	var reg ExemptionRegistry
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&reg); err != nil {
		return nil, fmt.Errorf("parse exemptions %s: %w", p, err)
	}
	return &reg, nil
}

func validateExemptions(root string, reg *ExemptionRegistry, now time.Time) []string {
	var errs []string
	seen := map[string]bool{}
	for i, ex := range reg.Exemptions {
		f := filepath.ToSlash(strings.TrimSpace(ex.File))
		if f == "" {
			errs = append(errs, fmt.Sprintf("entry %d: file is empty", i))
			continue
		}
		if seen[f] {
			errs = append(errs, fmt.Sprintf("%s: duplicate exemption entry", f))
		}
		seen[f] = true
		if strings.TrimSpace(ex.Reason) == "" {
			errs = append(errs, fmt.Sprintf("%s: reason must not be empty", f))
		}
		if strings.TrimSpace(ex.Owner) == "" {
			errs = append(errs, fmt.Sprintf("%s: owner must not be empty", f))
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(f))); err != nil {
			errs = append(errs, fmt.Sprintf("%s: exempted file does not exist", f))
		}
		if strings.TrimSpace(ex.Expires) != "" {
			exp, err := time.Parse("2006-01-02", strings.TrimSpace(ex.Expires))
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: invalid expires date %q (want YYYY-MM-DD)", f, ex.Expires))
			} else if isExpired(exp, now) {
				errs = append(errs, fmt.Sprintf("%s: exemption expired on %s", f, ex.Expires))
			}
		}
	}
	return errs
}

func isExpired(exp, now time.Time) bool {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	return today.After(exp)
}

func activeExemption(reg *ExemptionRegistry, rel string, now time.Time) (ok bool, note string) {
	if reg == nil {
		return false, ""
	}
	for _, ex := range reg.Exemptions {
		if filepath.ToSlash(strings.TrimSpace(ex.File)) != rel {
			continue
		}
		if strings.TrimSpace(ex.Reason) == "" || strings.TrimSpace(ex.Owner) == "" {
			return false, "exemption has empty reason or owner"
		}
		if strings.TrimSpace(ex.Expires) != "" {
			exp, err := time.Parse("2006-01-02", strings.TrimSpace(ex.Expires))
			if err != nil {
				return false, fmt.Sprintf("exemption has invalid expires date %q", ex.Expires)
			}
			if isExpired(exp, now) {
				return false, fmt.Sprintf("exemption expired on %s", ex.Expires)
			}
		}
		return true, ""
	}
	return false, ""
}

func isGoCompilerRuleFile(rel string) (subsystem string, ok bool) {
	rel = path.Clean(filepath.ToSlash(rel))
	if !strings.HasSuffix(rel, ".go") {
		return "", false
	}
	base := path.Base(rel)
	if strings.HasSuffix(base, "_test.go") || strings.HasSuffix(base, "_gen.go") {
		return "", false
	}
	for _, sub := range compilerSubsystems {
		prefix := "internal/sketch/" + sub + "/"
		if !strings.HasPrefix(rel, prefix) {
			continue
		}
		rest := strings.TrimPrefix(rel, prefix)
		for _, seg := range strings.Split(rest, "/") {
			if seg == "testdata" || seg == "snapshot" {
				return "", false
			}
		}
		return sub, true
	}
	return "", false
}

func isErgoMirrorFile(rel, subsys string) bool {
	rel = path.Clean(filepath.ToSlash(rel))
	prefix := "compiler/" + subsys + "/"
	if !strings.HasPrefix(rel, prefix) || !strings.HasSuffix(rel, ".ergo") {
		return false
	}
	rest := strings.TrimPrefix(rel, prefix)
	for _, seg := range strings.Split(rest, "/") {
		if seg == "testdata" || seg == "snapshot" {
			return false
		}
	}
	return true
}

func evaluateDrift(_ string, changed []string, reg *ExemptionRegistry, now time.Time) DriftReport {
	goBySub := map[string][]string{}
	ergoMirrored := map[string]bool{}
	for _, rel := range changed {
		rel = path.Clean(filepath.ToSlash(rel))
		if sub, ok := isGoCompilerRuleFile(rel); ok {
			goBySub[sub] = append(goBySub[sub], rel)
			continue
		}
		for _, sub := range compilerSubsystems {
			if isErgoMirrorFile(rel, sub) {
				ergoMirrored[sub] = true
			}
		}
	}

	var report DriftReport
	for _, sub := range compilerSubsystems {
		files := goBySub[sub]
		if len(files) == 0 {
			continue
		}
		slices.Sort(files)
		if ergoMirrored[sub] {
			report.MirroredFiles += len(files)
			continue
		}
		for _, f := range files {
			if ok, note := activeExemption(reg, f, now); ok {
				report.ExemptedFiles++
			} else {
				report.Violations = append(report.Violations, DriftViolation{
					Subsystem: sub,
					File:      f,
					MirrorDir: "compiler/" + sub + "/",
					Note:      note,
				})
			}
		}
	}
	return report
}

func gitChangedFiles(root, base string) ([]string, error) {
	cmd := exec.Command("git", "--no-optional-locks", "-C", root, "diff", "--no-renames", "--name-only", "-z", base, "--")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git diff %s: %w: %s", base, err, strings.TrimSpace(stderr.String()))
	}
	raw := stdout.Bytes()
	if len(raw) == 0 {
		return nil, nil
	}
	var files []string
	for _, p := range bytes.Split(raw, []byte{0}) {
		if len(p) == 0 {
			continue
		}
		files = append(files, filepath.ToSlash(string(p)))
	}
	slices.Sort(files)
	return files, nil
}
