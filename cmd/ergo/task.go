package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/budget"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/lawcache"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/internal/task"
)

const taskHelp = `usage: ergo task --list [--affected BASE [--why]] [--json] [DIR]
       ergo task gen [--check] [--fresh] [--jobs N] [NAME...] [DIR]
       ergo task TASK [--fresh] [--affected BASE] [--json] [--why] [--jobs N] [DIR]

ergo task TASK runs a task of the workspace at DIR (default .): the
steps of its ergo.work task lines, or for gate with none written, check,
fmt --check, lint, test and gen --check (docs/spec/build.md, Tasks). A
built-in step runs once per member, each member after the members it
reaches; a step whose inputs hold since a run that passed replays its
verdict and does not run.

  --affected BASE  run only the steps that read a path changed since the
                   commit BASE (git diff BASE, untracked files included);
                   the others print unaffected and count as passed
  --fresh          ignore kept verdicts: every step runs, a kept pass the
                   fresh run fails is a contradiction, and a flaky test
                   (failed, then passed its one retry) fails the run.
                   Landings run ergo task gate --fresh.
  --json           one JSON object per step, then {"summary": …}
  --why            print each step that ran and the input that moved

Exit: 0 when every step passed (a flaky one too, but with --fresh); 1
when a step failed or a kept pass was contradicted; 2 for an invalid
invocation or an ergo.work that does not read.

ergo task gen runs the generators of the workspace at DIR (default .) in the
order their inputs and outputs give (docs/spec/build.md, Generators): each
gen line of DIR's ergo.work, and MEMBER:gen for each member holding a
package ergo gen wrote. A generator whose inputs are unchanged since a
run that held, and whose outputs still hash as that run wrote them, is a
hit and does not run; the hit says its class (vouched: declared reads for
a gen line, derived for a member's).

  --list    print the generators, the edges their last runs give
            (A before B: A writes a file B reads) and the order
  --affected BASE
            with --list: print instead the generators whose key over
            their last run differs between the commit BASE (read from
            Git, no checkout) and the working tree, or whose outputs
            differ, and those that run after them; --why says each one's
            first input that moved. Run at the repository's top folder.
  --check   write nothing: a generator that misses runs in a copy of the
            tree, and the run fails, naming the files, when the copy's
            generated files differ from DIR's
  --fresh   ignore hits: every generator runs; a stored result the fresh
            run contradicts fails the run, naming the generator (its
            reads lines are incomplete). Landings run --check --fresh.
  --jobs N  the cores taken from the host budget ($ERGO_BUDGET) and given
            to each generator as GOMAXPROCS (default 4)

Exit: 0 when every generator held (and with --check nothing is stale); 1
when one failed, a file is stale or a result was contradicted; 2 for an
invalid invocation.
`

func taskCommand(args []string) error {
	return taskMain(context.Background(), args, os.Stdout)
}

func taskMain(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("task", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	list := fs.Bool("list", false, "")
	asJSON := fs.Bool("json", false, "")
	check := fs.Bool("check", false, "")
	fresh := fs.Bool("fresh", false, "")
	jobs := fs.Int("jobs", 4, "")
	affected := fs.String("affected", "", "")
	why := fs.Bool("why", false, "")
	var rest []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return flagError(err, taskHelp)
		}
		args = fs.Args()
		if len(args) > 0 {
			rest = append(rest, args[0])
			args = args[1:]
		}
	}
	if *list {
		if *check || *fresh {
			return usageError{message: "task: --list runs nothing; it takes no --check or --fresh"}
		}
		if len(rest) > 1 {
			return usageError{message: "usage: ergo task --list [--affected BASE [--why]] [--json] [DIR]"}
		}
	} else if len(rest) == 0 {
		return usageError{message: "usage: ergo task TASK [--fresh] [--affected BASE] [--json] [DIR] | ergo task gen [--check] [--fresh] [NAME...] [DIR] | ergo task --list [--json] [DIR]"}
	} else if rest[0] != "gen" {
		if *check {
			return usageError{message: "task: --check is gen's; a task's gen --check step checks the generators"}
		}
		if *jobs < 1 {
			return usageError{message: "task: --jobs is at least 1"}
		}
		return runTask(ctx, rest[0], rest[1:], taskFlags{fresh: *fresh, asJSON: *asJSON, why: *why, affected: *affected, jobs: *jobs}, stdout)
	} else {
		if *affected != "" || *why {
			return usageError{message: "task gen: --affected and --why are a task's"}
		}
		rest = rest[1:]
	}
	if *jobs < 1 {
		return usageError{message: "task: --jobs is at least 1"}
	}
	dir := "."
	var names []string
	for _, a := range rest {
		if info, err := os.Stat(a); err == nil && info.IsDir() && !strings.Contains(a, ":") {
			dir = a
		} else {
			names = append(names, a)
		}
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return exitStatusError{code: 1, message: "ergo task: " + err.Error()}
	}
	work, err := sketchfront.ReadWork(root)
	if err != nil {
		return exitStatusError{code: 2, message: "ergo task: " + err.Error()}
	}
	gens := work.Gens
	members, elsewhere, err := memberNodes(root)
	if err != nil {
		return exitStatusError{code: 1, message: "ergo task: " + err.Error()}
	}
	if len(gens) == 0 && len(members) == 0 && !*list {
		return exitStatusError{code: 1, message: fmt.Sprintf("ergo task: %s holds no generator: no ergo.work gen line and no package ergo gen wrote", dir)}
	}
	var env []string
	var release func()
	if !*list {
		if env, release, err = taskBudget(ctx, *jobs, root); err != nil {
			return exitStatusError{code: 1, message: "ergo task: " + err.Error()}
		}
		defer release()
	}
	nodes, err := taskNodes(gens, members, names, task.StatedEnv(nil, env))
	if err != nil {
		return exitStatusError{code: 1, message: "ergo task: " + err.Error()}
	}
	r := &task.Runner{Root: root, Store: taskStore(), Out: stdout}
	if *list && *affected != "" {
		base, err := readGitBase(root, *affected)
		if err != nil {
			return exitStatusError{code: 1, message: "ergo task --affected: " + err.Error()}
		}
		got, err := r.Affected(nodes, base)
		if err != nil {
			return exitStatusError{code: 1, message: "ergo task --affected: " + err.Error()}
		}
		if *asJSON {
			if got == nil {
				got = []task.Affect{}
			}
			return json.NewEncoder(stdout).Encode(got)
		}
		for _, a := range got {
			if *why {
				fmt.Fprintf(stdout, "%s: %s\n", a.Name, a.Why)
			} else {
				fmt.Fprintln(stdout, a.Name)
			}
		}
		return nil
	}
	if *list {
		g, err := r.Graph(nodes)
		if err != nil {
			return exitStatusError{code: 1, message: "ergo task: " + err.Error()}
		}
		ws, err := sketchfront.DiscoverWorkspace(nil, root)
		if err != nil {
			return exitStatusError{code: 2, message: "ergo task: " + err.Error()}
		}
		plans, err := taskPlans(work, ws)
		if err != nil {
			return exitStatusError{code: 2, message: "ergo task: " + err.Error()}
		}
		if err := printTaskGraph(stdout, g, plans, *asJSON); err != nil {
			return err
		}
		if !*asJSON {
			owner, _ := ownedElsewhere(r, nodes, elsewhere)
			for _, p := range elsewhere {
				by := "gen " + owner[p.Pkg]
				if owner[p.Pkg] == "" {
					by = "no generator's last run (ergo task gen fails until one writes it)"
				}
				fmt.Fprintf(stdout, "%s: from %s, written by %s\n", p.Pkg, p.Contract, by)
			}
		}
		return nil
	}
	rep, err := r.Gen(ctx, nodes, task.Mode{Check: *check, Fresh: *fresh})
	if err != nil {
		return exitStatusError{code: 1, message: "ergo task: " + err.Error()}
	}
	// A package with ergo gen's header that no member node writes must be
	// another generator's output, or nothing keeps it current. With names
	// given, the generator that writes it may not have been asked for.
	if len(names) == 0 {
		if _, err := ownedElsewhere(r, nodes, elsewhere); err != nil {
			return exitStatusError{code: 1, message: "ergo task: " + err.Error()}
		}
	}
	if len(rep.Stale) > 0 {
		return exitStatusError{code: 1, message: "ergo task gen --check: stale: " + strings.Join(rep.Stale, ", ") + "; run ergo task gen"}
	}
	if len(rep.Contradicted) > 0 {
		return exitStatusError{code: 1, message: "ergo task gen --fresh: a stored result differs from a fresh run of " + strings.Join(rep.Contradicted, ", ") + ": add the files it reads to its reads lines in ergo.work"}
	}
	return nil
}

// taskNodes are the nodes of the gen lines, then the members' derived
// ones, only those named when names is not empty.
func taskNodes(gens []sketchfront.WorkGen, members []task.Node, names []string, env task.ChildEnv) ([]task.Node, error) {
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	var nodes []task.Node
	for _, g := range gens {
		if len(want) > 0 && !want[g.Name] {
			continue
		}
		delete(want, g.Name)
		n, err := task.GoNode(task.GoGen{Name: g.Name, Command: g.Command, Reads: g.Reads}, env)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %v", sketchfront.WorkFile, g.Line, err)
		}
		nodes = append(nodes, n)
	}
	for _, m := range members {
		if len(want) > 0 && !want[m.Name] {
			continue
		}
		delete(want, m.Name)
		nodes = append(nodes, m)
	}
	for n := range want {
		return nil, fmt.Errorf("no gen line names %s", n)
	}
	return nodes, nil
}

// taskStore is the store of task results: the law-verdict store's folder
// under its own identity, so a rebuilt ergo keeps them; nil (nothing kept)
// when ERGO_LAWCACHE is off or there is no cache root.
func taskStore() *lawcache.Store {
	if os.Getenv("ERGO_LAWCACHE") == "off" {
		return nil
	}
	root, err := build.DefaultRoot()
	if err != nil {
		return nil
	}
	return lawcache.Open(root, task.StoreIdentity)
}

// taskBudget takes width cores from the host budget the ERGO_BUDGET file
// names, once for the whole run, and gives them to each generator: its
// GOMAXPROCS and the lent grant, so that a generator's own ergo budget
// takes nothing. With no budget file it takes nothing and gives
// GOMAXPROCS only.
func taskBudget(ctx context.Context, width int, root string) ([]string, func(), error) {
	env := []string{"GOMAXPROCS=" + strconv.Itoa(width)}
	path := os.Getenv("ERGO_BUDGET")
	if path == "" {
		return env, func() {}, nil
	}
	cfg, err := budget.LoadConfig(path)
	if err != nil {
		return nil, nil, err
	}
	b, err := budget.Open(cfg)
	if err != nil {
		return nil, nil, err
	}
	lane := os.Getenv("ERGO_BUDGET_LANE")
	if lane == "" {
		lane = filepath.Base(root)
	}
	g, err := b.Take(ctx, budget.Request{Width: width, Crit: os.Getenv("ERGO_BUDGET_CLASS") == "crit", Lane: lane})
	if err != nil {
		return nil, nil, err
	}
	env = []string{"GOMAXPROCS=" + strconv.Itoa(g.Width()), g.Env()}
	// The members' gen nodes run in this process, in the grant too.
	runtime.GOMAXPROCS(g.Width())
	return env, g.Release, nil
}

// taskGraphJSON is --list --json's answer.
type taskGraphJSON struct {
	Gens []taskGenJSON `json:"gens"`
	// Tasks are the tasks and the steps each runs, in order.
	Tasks []taskPlanJSON `json:"tasks"`
	Order []string       `json:"order"`
	// Cycles are the generators that read each other's outputs, each in
	// the order they run until they settle.
	Cycles [][]string `json:"cycles"`
}

type taskGenJSON struct {
	Name  string `json:"name"`
	Class string `json:"class"`
	// Known is false until the gen has run once: its outputs, and so its
	// edges, are unknown.
	Known bool     `json:"known"`
	After []string `json:"after"`
}

// taskPlanJSON is one task of --list: its steps in the order they run,
// each "STEP @ MEMBER" ("." for the workspace).
type taskPlanJSON struct {
	Name  string   `json:"name"`
	After []string `json:"after"`
	Env   []string `json:"env"`
	Steps []string `json:"steps"`
}

// taskPlans are the plans of work's tasks over ws.
func taskPlans(work sketchfront.WorkDecl, ws sketchfront.Workspace) ([]taskPlanJSON, error) {
	var out []taskPlanJSON
	for _, t := range work.Tasks {
		plan, err := taskPlan(work, t.Name, ws.Order())
		if err != nil {
			return nil, err
		}
		p := taskPlanJSON{Name: t.Name, After: append([]string{}, t.After...), Env: append([]string{}, t.Env...), Steps: []string{}}
		for _, s := range plan {
			member := "."
			if s.member >= 0 {
				member = ws.Members[s.member].Rel
			}
			p.Steps = append(p.Steps, s.task+": "+strings.Join(s.step.Words, " ")+" @ "+member)
		}
		out = append(out, p)
	}
	return out, nil
}

func printTaskGraph(w io.Writer, g task.Graph, plans []taskPlanJSON, asJSON bool) error {
	after := make([][]string, len(g.Nodes))
	for _, e := range g.Edges {
		after[e[1]] = append(after[e[1]], g.Nodes[e[0]].Name)
	}
	out := taskGraphJSON{Gens: []taskGenJSON{}, Tasks: plans, Order: []string{}, Cycles: [][]string{}}
	for _, c := range g.Cycles {
		var names []string
		for _, i := range c {
			names = append(names, g.Nodes[i].Name)
		}
		out.Cycles = append(out.Cycles, names)
	}
	for i, n := range g.Nodes {
		a := after[i]
		if a == nil {
			a = []string{}
		}
		out.Gens = append(out.Gens, taskGenJSON{Name: n.Name, Class: string(n.Class), Known: g.Known[i], After: a})
	}
	for _, i := range g.Order {
		out.Order = append(out.Order, g.Nodes[i].Name)
	}
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	for _, gen := range out.Gens {
		line := "gen " + gen.Name + " (" + gen.Class + ")"
		switch {
		case !gen.Known:
			line += ": not run yet, edges unknown"
		case len(gen.After) > 0:
			line += ": after " + strings.Join(gen.After, ", ")
		}
		fmt.Fprintln(w, line)
	}
	for _, c := range out.Cycles {
		fmt.Fprintln(w, "cycle: "+strings.Join(c, ", ")+" (each reads another's outputs; run in this order until they settle)")
	}
	fmt.Fprintln(w, "order: "+strings.Join(out.Order, ", "))
	for _, t := range plans {
		line := "task " + t.Name
		if len(t.After) > 0 {
			line += " (after " + strings.Join(t.After, ", ") + ")"
		}
		if len(t.Env) > 0 {
			line += " (env " + strings.Join(t.Env, " ") + ")"
		}
		fmt.Fprintln(w, line+":")
		for _, s := range t.Steps {
			fmt.Fprintln(w, "  "+s)
		}
	}
	return nil
}

// taskFlags are a task run's flags.
type taskFlags struct {
	fresh, asJSON, why bool
	affected           string
	jobs               int
}

// runTask is ergo task NAME: the task's plan over the workspace at the
// folder args names (default .), run by the verdict runner.
func runTask(ctx context.Context, name string, args []string, f taskFlags, stdout io.Writer) error {
	if len(args) > 1 {
		return usageError{message: "usage: ergo task TASK [--fresh] [--affected BASE] [--json] [--why] [DIR]"}
	}
	dir := "."
	if len(args) == 1 {
		dir = args[0]
	}
	root, err := filepath.Abs(dir)
	if err == nil {
		root, err = filepath.EvalSymlinks(root)
	}
	if err != nil {
		return exitStatusError{code: 1, message: "ergo task: " + err.Error()}
	}
	work, err := sketchfront.ReadWork(root)
	if err != nil {
		return exitStatusError{code: 2, message: "ergo task: " + err.Error()}
	}
	if _, ok := work.Task(name); !ok {
		return usageError{message: fmt.Sprintf("ergo task: no task %s; %s at %s has %s", name, sketchfront.WorkFile, dir, taskNames(work))}
	}
	ws, err := sketchfront.DiscoverWorkspace(nil, root)
	if err != nil {
		return exitStatusError{code: 2, message: "ergo task: " + err.Error()}
	}
	plan, err := taskPlan(work, name, ws.Order())
	if err != nil {
		return exitStatusError{code: 2, message: "ergo task: " + err.Error()}
	}
	members, orphan, err := memberNodes(root)
	if err != nil {
		return exitStatusError{code: 1, message: "ergo task: " + err.Error()}
	}
	budgetEnv, release, err := taskBudget(ctx, f.jobs, root)
	if err != nil {
		return exitStatusError{code: 1, message: "ergo task: " + err.Error()}
	}
	defer release()
	gate, err := newGateWorkspace(root, ws)
	if err != nil {
		return exitStatusError{code: 1, message: "ergo task: " + err.Error()}
	}
	logs, flaky := taskLogs()
	runner := &task.Runner{Root: root, Store: taskStore(), Out: io.Discard, Logs: logs, Flaky: flaky,
		// test writes the law ledgers check and lint read.
		RunWrites: func(part string) bool { return part == "law ledger" || path.Base(part) == lower.LedgerFile }}
	tr := &taskRun{root: root, work: work, ws: ws, gate: gate, runner: runner, fresh: f.fresh, orphan: orphan,
		genNodes: func(names, env []string) ([]task.Node, error) {
			return taskNodes(work.Gens, members, names, task.StatedEnv(env, budgetEnv))
		}}
	mode := task.VerdictMode{Fresh: f.fresh}
	if f.affected != "" {
		changed, err := task.Changed(ctx, root, f.affected)
		if err != nil {
			return exitStatusError{code: 2, message: "ergo task: --affected " + f.affected + ": " + err.Error()}
		}
		mode.Affected, mode.Changed = true, changed
	}
	rep, err := runner.Verdicts(ctx, tr.steps(plan), mode)
	if err != nil {
		return exitStatusError{code: 1, message: "ergo task: " + err.Error()}
	}
	failed, err := reportTask(stdout, name, rep, gate.first, f.fresh, f.asJSON, f.why)
	if err != nil {
		return err
	}
	if failed {
		return exitStatusError{code: 1, handled: true}
	}
	return nil
}
