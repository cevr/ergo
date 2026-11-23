package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/lawcache"
	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/internal/task"
)

// planned is one step of a task's plan: a built-in verb at a member
// (member -1 for gen --check, which runs over the workspace), a gen
// line's generator, or the generators (all of them, writing).
type planned struct {
	task   string
	step   sketchfront.WorkStep
	member int
	// env are the variables the task's env lines name.
	env []string
	// after are earlier plan indexes that must pass before this step.
	after []int
}

// taskPlan is the steps of task name in the order they run: first each
// task it runs after (its after lines, each such task's plan in turn),
// then its steps in order, a step naming a task standing for that task's
// plan, and a built-in step at each member in the workspace's order (each
// member after the members it reaches). A task runs once per plan, the
// first time it is reached.
func taskPlan(work sketchfront.WorkDecl, name string, order []int) ([]planned, error) {
	var out []planned
	done := map[string][]int{}
	var expand func(name string, inherited []int) ([]int, error)
	expand = func(name string, inherited []int) ([]int, error) {
		if indexes, ok := done[name]; ok {
			return indexes, nil
		}
		var t sketchfront.WorkTask
		if name == sketchfront.GenTask {
			t.Steps = []sketchfront.WorkStep{{Words: []string{"gen"}, Verb: "gen"}}
		} else {
			var ok bool
			if t, ok = work.Task(name); !ok {
				return nil, fmt.Errorf("no task %s: %s names %s", name, sketchfront.WorkFile, taskNames(work))
			}
		}
		after := slices.Clone(inherited)
		for _, a := range t.After {
			require, err := expand(a, inherited)
			if err != nil {
				return nil, err
			}
			after = append(after, require...)
		}
		results := slices.Clone(after)
		add := func(step sketchfront.WorkStep, member int) {
			results = append(results, len(out))
			out = append(out, planned{task: name, step: step, member: member, env: t.Env, after: after})
		}
		for _, s := range t.Steps {
			switch {
			case s.Task != "":
				indexes, err := expand(s.Task, after)
				if err != nil {
					return nil, err
				}
				results = append(results, indexes...)
			case s.Gen != "", s.Verb == "gen":
				add(s, -1)
			default:
				for _, i := range order {
					add(s, i)
				}
			}
		}
		done[name] = results
		return results, nil
	}
	_, err := expand(name, nil)
	return out, err
}

// taskNames are the tasks work declares, for an error.
func taskNames(work sketchfront.WorkDecl) string {
	var names []string
	for _, t := range work.Tasks {
		names = append(names, t.Name)
	}
	return strings.Join(append(names, sketchfront.GenTask), ", ")
}

// taskRun is what runTask needs besides the plan.
type taskRun struct {
	root   string
	work   sketchfront.WorkDecl
	ws     sketchfront.Workspace
	gate   *gateWorkspace
	runner *task.Runner
	fresh  bool
	orphan []Elsewhere
	// genNodes makes the generator nodes for names (all when empty), in a
	// child environment with the task's env lines.
	genNodes func(names, env []string) ([]task.Node, error)
}

// steps are the task.Steps of a plan.
func (r *taskRun) steps(plan []planned) []task.Step {
	var out []task.Step
	for _, p := range plan {
		p := p
		verb := strings.Join(p.step.Words, " ")
		member := "."
		if p.member >= 0 {
			member = r.ws.Members[p.member].Rel
		}
		s := task.Step{Task: p.task, Verb: verb, Member: member, Class: task.Derived, After: p.after}
		if len(p.env) > 0 {
			s.Verb += " (env " + strings.Join(p.env, " ") + ")"
		}
		id := s.ID()
		run := func(f func(string, int) (bool, error)) func(context.Context) (bool, []byte, error) {
			return func(context.Context) (bool, []byte, error) {
				delete(r.gate.first, id)
				return captured(func() (bool, error) { return f(id, p.member) })
			}
		}
		switch {
		case p.step.Verb == "check" || p.step.Verb == "lint" || p.step.Verb == "test":
			s.Key = func(context.Context) (task.Key, error) {
				k, err := r.gate.moduleKey(p.member, p.env)
				if err != nil {
					return k, err
				}
				if p.step.Verb == "lint" {
					k.Parts = append(k.Parts, task.Part{Name: "lint config", Digest: lawcache.Key(gateLintConfig(r.ws.Members[p.member].Dir))})
				}
				return k, nil
			}
			s.Run = run(map[string]func(string, int) (bool, error){"check": r.gate.check, "lint": r.gate.lint, "test": r.gate.test}[p.step.Verb])
			s.Retry = p.step.Verb == "test"
		case p.step.Verb == "fmt":
			s.Key = func(context.Context) (task.Key, error) { return r.gate.fmtKey(p.member) }
			s.Run = run(r.gate.format)
		default:
			// A generator step: gen --check over every generator, a gen
			// line's, or the generators, writing. Its own store replays
			// each generator whose inputs hold.
			check := p.step.Verb == "gen" && p.task != sketchfront.GenTask
			var names []string
			if p.step.Gen != "" {
				names = []string{p.step.Gen}
			}
			s.Class = task.Vouched
			s.Paths = func(context.Context) ([]string, error) {
				nodes, err := r.genNodes(names, p.env)
				if err != nil {
					return nil, err
				}
				var paths []string
				for _, n := range nodes {
					in, ok := r.runner.LastInputs(n)
					if !ok {
						return []string{"."}, nil
					}
					paths = append(paths, in.Files...)
					paths = append(paths, in.Dirs...)
				}
				return paths, nil
			}
			s.Run = func(ctx context.Context) (bool, []byte, error) {
				nodes, err := r.genNodes(names, p.env)
				if err != nil {
					return false, nil, err
				}
				var log bytes.Buffer
				gr := *r.runner
				gr.Out = &log
				rep, err := gr.Gen(ctx, nodes, task.Mode{Check: check, Fresh: r.fresh})
				if err != nil {
					fmt.Fprintln(&log, err)
					r.gate.first[id] = err.Error()
					return false, log.Bytes(), nil
				}
				if len(rep.Contradicted) > 0 {
					r.gate.first[id] = "fresh generation contradicted the kept outputs of " + strings.Join(rep.Contradicted, ", ")
					return false, log.Bytes(), nil
				}
				if len(rep.Stale) > 0 {
					r.gate.first[id] = "stale: " + strings.Join(rep.Stale, ", ") + "; run ergo task gen"
					fmt.Fprintln(&log, r.gate.first[id])
					return false, log.Bytes(), nil
				}
				if len(names) == 0 {
					if _, err := ownedElsewhere(&gr, nodes, r.orphan); err != nil {
						r.gate.first[id] = err.Error()
						fmt.Fprintln(&log, err)
						return false, log.Bytes(), nil
					}
				}
				return true, log.Bytes(), nil
			}
		}
		out = append(out, s)
	}
	return out
}

// taskSummary is --json's last line.
type taskSummary struct {
	Verdict    string            `json:"verdict"`
	Passed     int               `json:"passed"`
	Failed     []task.StepResult `json:"failed"`
	Flaky      []task.StepResult `json:"flaky"`
	Replayed   int               `json:"replayed"`
	Unaffected int               `json:"unaffected"`
}

// stepLine is --json's object per node: the runner's result and the
// failure's first line.
type stepLine struct {
	task.StepResult
	First string `json:"first,omitempty"`
}

// reportTask prints the run: in text, one line per failed or flaky step
// (and, with why, per step that ran and why), then the summary; in JSON
// one object per step and the summary last. It answers whether the run
// failed.
func reportTask(w io.Writer, name string, rep task.VerdictReport, first map[string]string, fresh, asJSON, why bool) (bool, error) {
	sum := taskSummary{Failed: []task.StepResult{}, Flaky: []task.StepResult{}}
	enc := json.NewEncoder(w)
	for _, s := range rep.Steps {
		id := task.Step{Verb: s.Step, Member: s.Member}.ID()
		switch {
		case s.Verdict == task.Fail || s.Contradicted:
			sum.Failed = append(sum.Failed, s)
		case s.Verdict == task.Flaky:
			sum.Flaky = append(sum.Flaky, s)
			sum.Passed++
		case s.Verdict == task.Unaffected:
			sum.Unaffected++
			sum.Passed++
		default:
			sum.Passed++
		}
		if s.Replay == "hit" {
			sum.Replayed++
		}
		if asJSON {
			if err := enc.Encode(stepLine{StepResult: s, First: first[id]}); err != nil {
				return false, err
			}
			continue
		}
		switch {
		case s.BlockedBy != "":
			fmt.Fprintf(w, "FAIL %s %s: %s\n", s.Step, s.Member, s.BlockedBy)
		case s.Contradicted:
			fmt.Fprintf(w, "FAIL %s %s: a kept pass that a fresh run fails (its key misses an input): %s\n", s.Step, s.Member, first[id])
		case s.Verdict == task.Fail:
			fmt.Fprintf(w, "FAIL %s %s: %s\n", s.Step, s.Member, firstOr(first[id], s.Log))
		case s.Verdict == task.Flaky:
			fmt.Fprintf(w, "flaky %s %s: seed %s, log %s\n", s.Step, s.Member, firstOr(s.Seed, "none"), s.Log)
		case why && s.Replay == "miss":
			fmt.Fprintf(w, "ran %s %s: %s\n", s.Step, s.Member, s.Why)
		}
	}
	failed := rep.Failed(fresh)
	sum.Verdict = "pass"
	if failed {
		sum.Verdict = "fail"
	}
	if fresh {
		// --fresh counts a flaky step as a failure.
		sum.Passed -= len(sum.Flaky)
		sum.Failed = append(sum.Failed, sum.Flaky...)
	}
	if asJSON {
		return failed, enc.Encode(map[string]taskSummary{"summary": sum})
	}
	fmt.Fprintf(w, "%s: %d passed (%d replayed, %d unaffected), %d failed, %d flaky\n", name, sum.Passed, sum.Replayed, sum.Unaffected, len(sum.Failed), len(sum.Flaky))
	return failed, nil
}

func firstOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// taskLogs is the folder of the steps' logs and the flaky file, below
// the cache root; "" when there is none.
func taskLogs() (logs, flaky string) {
	root, err := build.DefaultRoot()
	if err != nil {
		return "", ""
	}
	return filepath.Join(root, "task", "logs"), filepath.Join(root, "task", "flaky.txt")
}
