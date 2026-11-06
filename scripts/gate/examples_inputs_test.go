package main

import (
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The examples step keys every Go file of the module that its runner
// links: for each package of the module in `go list -deps
// ./scripts/examples`, each compiled Go file and each embedded file is one
// of the step's inputs. A runner that imports the checker (batch 9 D3)
// keys on the checker's sources, so a checker edit cannot replay a stale
// examples pass. go list reads the runner's imports, so the test reads
// what the runner links now, not a list.
func TestExamplesStepKeysTheRunnersClosure(t *testing.T) {
	root, steps := repositorySteps(t)
	at := slices.IndexFunc(steps, func(s *Step) bool { return s.Name == "examples (default)" })
	if at < 0 || steps[at].Inputs == nil {
		t.Fatal("no keyed step 'examples (default)'")
	}
	inputs := steps[at].Inputs
	command := exec.Command("go", "list", "-deps", "-f", "{{if not .Standard}}{{.Dir}}\t{{join .GoFiles \" \"}}\t{{join .EmbedFiles \" \"}}{{end}}", "./scripts/examples")
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		t.Fatalf("go list -deps ./scripts/examples: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	files := 0
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		// Dir, Go files, embedded files; an empty last field may be
		// trimmed.
		fields := append(strings.Split(line, "\t"), "", "")
		if fields[0] == "" {
			continue
		}
		dir := fields[0]
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			dir = real
		}
		rel, err := filepath.Rel(resolved, dir)
		if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			continue
		}
		for _, name := range append(strings.Fields(fields[1]), strings.Fields(fields[2])...) {
			file := filepath.ToSlash(filepath.Join(rel, name))
			files++
			if !selected(file, inputs) {
				t.Errorf("the examples runner links %s, which the step's inputs do not select: an edit of it would replay a stale pass", file)
			}
		}
	}
	if files == 0 {
		t.Fatal("go list names no Go file of the runner")
	}
}
