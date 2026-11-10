package main

import (
	"fmt"
	"os"
	"strings"

	"ergo.local/prototype/internal/sketch/machinegraph"
	"ergo.local/prototype/internal/sketch/syntax"
)

const inspectMachineHelp = `usage: ergo inspect machine FILE|DIR [--json]

Draws the graph of each machine of the root package (runtime.md, Machines
and actors 4 and 15): its states and signals, each arm of its step with
the state and signal variants it takes and the decisions it can give
(kind, target state, position), the timer and the work of each state, and
the pairs a run can reach with the arm that steps each. --json prints the
graphs as JSON, each with its reachable pairs.

Exit status: 0 when the package checks, 1 when it does not, 2 on an
invalid invocation.
`

// inspectCommand runs `ergo inspect machine` when its first word is
// machine, and else prints what the program reaches of Go
// (inspectGoCommand); every other query of the designed `ergo inspect`
// stays removed in v1 (#203).
func inspectCommand(args []string) error {
	if len(args) == 0 || args[0] != "machine" {
		return inspectGoCommand(args)
	}
	if rest := args[1:]; len(rest) == 1 && (rest[0] == "--help" || rest[0] == "-h") {
		_, err := os.Stdout.WriteString(inspectMachineHelp)
		return err
	}
	return inspectMachineCommand(args[1:])
}

// inspectMachineCommand prints the machine graphs of the root package.
func inspectMachineCommand(args []string) error {
	opts, err := parseFileOptions("inspect machine", args)
	if err != nil {
		return invalidInvocation(err)
	}
	l, err := load(opts.file, opts.target)
	if err != nil {
		return err
	}
	root := l.analysis.Root()
	if root.Package == nil {
		return fmt.Errorf("inspect machine: %s does not check; run ergo check", opts.file)
	}
	at := func(decl syntax.Node, pos syntax.Pos) machinegraph.Site {
		for _, f := range root.Files {
			if f.Syntax == nil {
				continue
			}
			for _, d := range f.Syntax.Decls {
				if d == decl {
					name := f.Rel
					if name == "" {
						name = l.source.String()
					}
					return machinegraph.Site{File: name, Line: f.Syntax.Line(pos)}
				}
			}
		}
		return machinegraph.Site{File: "?", Line: 0}
	}
	graphs := machinegraph.Machines(root.Package, at)
	if opts.json {
		type out struct {
			*machinegraph.Graph
			Reachable []machinegraph.Pair
		}
		list := make([]out, len(graphs))
		for i, g := range graphs {
			list[i] = out{g, g.Reachable()}
		}
		return printJSON(list)
	}
	var b strings.Builder
	for _, g := range graphs {
		b.WriteString(g.Text())
		for _, p := range g.Reachable() {
			sites := make([]string, len(p.Arms))
			for i, s := range p.Arms {
				sites[i] = s.String()
			}
			arms := strings.Join(sites, " or ")
			if arms == "" {
				arms = "no arm"
			}
			fmt.Fprintf(&b, "  reach (%s, %s) by %s\n", p.State, p.Signal, arms)
		}
	}
	if len(graphs) == 0 {
		b.WriteString("no machine\n")
	}
	_, err = os.Stdout.WriteString(b.String())
	return err
}
