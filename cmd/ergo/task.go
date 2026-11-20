package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/budget"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/lawcache"
	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/internal/task"
)

const taskHelp = `usage: ergo task --list [--json] [DIR]
       ergo task gen [--check] [--fresh] [--jobs N] [NAME...] [DIR]

ergo task runs the generators of the workspace at DIR (default .) in the
order their inputs and outputs give (docs/spec/build.md, Generators): each
gen line of DIR's ergo.work. A generator whose inputs are unchanged since
a run that held, and whose outputs still hash as that run wrote them, is
a hit and does not run; the hit says its class (vouched: declared reads).

  --list    print the generators, the edges their last runs give
            (A before B: A writes a file B reads) and the order
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
			return usageError{message: "usage: ergo task --list [--json] [DIR]"}
		}
	} else if len(rest) == 0 || rest[0] != "gen" {
		return usageError{message: "usage: ergo task gen [--check] [--fresh] [NAME...] [DIR] | ergo task --list [--json] [DIR]"}
	} else {
		rest = rest[1:]
	}
	if *jobs < 1 {
		return usageError{message: "task: --jobs is at least 1"}
	}
	dir := "."
	var names []string
	for _, a := range rest {
		if strings.ContainsAny(a, "/.") {
			dir = a
		} else {
			names = append(names, a)
		}
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return exitStatusError{code: 1, message: "ergo task: " + err.Error()}
	}
	gens, err := sketchfront.ReadWorkFile(root)
	if err != nil {
		return exitStatusError{code: 1, message: "ergo task: " + err.Error()}
	}
	if len(gens) == 0 {
		return exitStatusError{code: 1, message: fmt.Sprintf("ergo task: %s holds no ergo.work gen line", dir)}
	}
	var env []string
	var release func()
	if !*list {
		if env, release, err = taskBudget(ctx, *jobs, root); err != nil {
			return exitStatusError{code: 1, message: "ergo task: " + err.Error()}
		}
		defer release()
	}
	nodes, err := taskNodes(gens, names, env)
	if err != nil {
		return exitStatusError{code: 1, message: "ergo task: " + err.Error()}
	}
	r := &task.Runner{Root: root, Store: taskStore(), Out: stdout}
	if *list {
		g, err := r.Graph(nodes)
		if err != nil {
			return exitStatusError{code: 1, message: "ergo task: " + err.Error()}
		}
		return printTaskGraph(stdout, g, *asJSON)
	}
	rep, err := r.Gen(ctx, nodes, task.Mode{Check: *check, Fresh: *fresh})
	if err != nil {
		return exitStatusError{code: 1, message: "ergo task: " + err.Error()}
	}
	if len(rep.Stale) > 0 {
		return exitStatusError{code: 1, message: "ergo task gen --check: stale: " + strings.Join(rep.Stale, ", ") + "; run ergo task gen"}
	}
	if len(rep.Contradicted) > 0 {
		return exitStatusError{code: 1, message: "ergo task gen --fresh: a stored result differs from a fresh run of " + strings.Join(rep.Contradicted, ", ") + ": add the files it reads to its reads lines in ergo.work"}
	}
	return nil
}

// taskNodes are the nodes of the gen lines, only those named when names
// is not empty.
func taskNodes(gens []sketchfront.WorkGen, names, env []string) ([]task.Node, error) {
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
	return env, g.Release, nil
}

// taskGraphJSON is --list --json's answer.
type taskGraphJSON struct {
	Gens  []taskGenJSON `json:"gens"`
	Order []string      `json:"order"`
}

type taskGenJSON struct {
	Name  string `json:"name"`
	Class string `json:"class"`
	// Known is false until the gen has run once: its outputs, and so its
	// edges, are unknown.
	Known bool     `json:"known"`
	After []string `json:"after"`
}

func printTaskGraph(w io.Writer, g task.Graph, asJSON bool) error {
	after := make([][]string, len(g.Nodes))
	for _, e := range g.Edges {
		after[e[1]] = append(after[e[1]], g.Nodes[e[0]].Name)
	}
	out := taskGraphJSON{Gens: []taskGenJSON{}, Order: []string{}}
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
	fmt.Fprintln(w, "order: "+strings.Join(out.Order, ", "))
	return nil
}
