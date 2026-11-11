package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// exploredFile names, in a program's folder, the tests and laws whose
// schedules the explore leg searches (runtime.md, Explored schedules): one
// line each, `PACKAGE NAME FLAG...`, PACKAGE the package folder relative
// to the program ("." for a program of one folder), NAME the test or law
// `--run` selects and FLAG the search, which starts with `--explore` and
// a mode (dfs, pct or random) and gives its bounds; a line starting "#"
// says why. The leg runs `ergo test PACKAGE --run NAME FLAG...` and passes
// when the search finds no failure and the run ran NAME alone. When it
// fails, the leg runs the replay command the failure prints and says
// whether the schedule replays to the same failure.
const exploredFile = "explored"

// explored is one line of a program's explored file.
type explored struct {
	target string // the package to test, as the test leg names it
	label  string // the line's package as written
	name   string
	flags  []string
}

// readExplored reads p's explored file, if it has one.
func readExplored(p *program) ([]explored, []string) {
	data, err := os.ReadFile(filepath.Join(p.dir, exploredFile))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, []string{err.Error()}
	}
	var out []explored
	var problems []string
	for i, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if line = strings.TrimSpace(line); line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		words := strings.Fields(line)
		if len(words) < 4 || words[2] != "--explore" || !slices.Contains([]string{"dfs", "pct", "random"}, words[3]) {
			problems = append(problems, fmt.Sprintf("%s:%d: want PACKAGE NAME --explore dfs|pct|random [FLAG...], got %q", exploredFile, i+1, line))
			continue
		}
		target := p.main
		if p.module || words[0] != "." {
			target = filepath.Join(p.dir, filepath.FromSlash(words[0]))
		}
		if info, err := os.Stat(target); err != nil || (p.module && !info.IsDir()) {
			problems = append(problems, fmt.Sprintf("%s:%d: no package %s", exploredFile, i+1, words[0]))
			continue
		}
		out = append(out, explored{target: target, label: words[0], name: words[1], flags: words[2:]})
	}
	return out, problems
}

// exploreProgram runs the explore leg of each line of p's explored file.
func (r *runner) exploreProgram(p *program) {
	for _, e := range p.explored {
		leg := "explore " + e.label + " " + e.name
		o, err := r.tool(r.cfg.timeout, append([]string{"test", e.target, "--run", e.name}, e.flags...)...)
		switch {
		case err != nil:
			fmt.Fprintf(r.out, "FAIL %s: %s: could not start: %v\n", p.name, leg, err)
		case o.overCap() != "":
			fmt.Fprintf(r.out, "FAIL %s: %s: %s\n", p.name, leg, o.overCap())
		case o.passed():
			if why := selectedAlone(o.stdout, e.name); why != "" {
				fmt.Fprintf(r.out, "FAIL %s: %s: --run %s %s\n", p.name, leg, e.name, why)
				break
			}
			fmt.Fprintf(r.out, "ok %s: %s (%s)\n", p.name, leg, seconds(o.elapsed))
			continue
		default:
			fmt.Fprintf(r.out, "FAIL %s: %s%s\n", p.name, leg, o.why(r.cfg.timeout))
			r.printOutput(o, nil)
			fmt.Fprintf(r.out, "    %s\n", r.replays(o))
		}
		r.fail(p.name + ": " + leg)
	}
}

// selectedAlone says how an explored run's output shows that --run name
// did not run name alone: a verdict line of another test or law, or no
// verdict line of name; "" when it ran name alone.
func selectedAlone(stdout []byte, name string) string {
	ran := false
	for _, line := range strings.Split(string(stdout), "\n") {
		for _, p := range []string{"ok ", "FAIL "} {
			rest, ok := strings.CutPrefix(line, p)
			if !ok || strings.HasPrefix(rest, "explore ") || strings.HasPrefix(rest, "(") {
				continue
			}
			rest = strings.TrimPrefix(rest, "law ")
			if rest != name && !strings.HasPrefix(rest, name+" ") && !strings.HasPrefix(rest, name+":") {
				return fmt.Sprintf("ran %q too", line)
			}
			ran = true
		}
	}
	if !ran {
		return "printed no verdict of it"
	}
	return ""
}

// replays runs the replay command of a failed search, the output's
// "replay: ergo test …" line, and says whether it fails with the search's
// failure: the same "FAIL explore" verdict, less its counters.
func (r *runner) replays(o outcome) string {
	var replay, verdict string
	for _, line := range strings.Split(string(o.stdout), "\n") {
		if rest, ok := strings.CutPrefix(line, "replay: ergo "); ok {
			replay = rest
		}
		if rest, ok := strings.CutPrefix(line, "FAIL explore "); ok && verdict == "" {
			verdict = failureOf(rest)
		}
	}
	if replay == "" {
		return "the failure printed no replay command"
	}
	words := strings.Fields(replay)
	for i, w := range words {
		words[i] = strings.TrimSuffix(strings.TrimPrefix(w, "'"), "'")
	}
	again, err := r.tool(r.cfg.timeout, words...)
	if err != nil {
		return "the replay could not start: " + err.Error()
	}
	for _, line := range strings.Split(string(again.stdout), "\n") {
		if rest, ok := strings.CutPrefix(line, "FAIL explore replay"); ok && failureOf(rest) == verdict {
			return "the schedule replays to the same failure: ergo " + replay
		}
	}
	return "the schedule does not replay to the same failure: ergo " + replay
}

// failureOf is a FAIL explore verdict's failure: the text after the mode
// and before its counters.
func failureOf(rest string) string {
	_, rest, _ = strings.Cut(rest, ": ")
	if i := strings.LastIndex(rest, " ("); i >= 0 {
		rest = rest[:i]
	}
	return rest
}
