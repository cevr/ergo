package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"ergo.local/prototype/internal/linked/code"

	"ergo.local/prototype/internal/sketch/api"
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
` + "\n" + inspectAPIHelp

const inspectAPIHelp = `usage: ergo inspect api FILE|DIR [--json]
       ergo inspect api --diff OLD NEW [--json]

Shows exported functions and methods from checked source: fails, needs,
takes (written or inferred), and generic copy sites (plain data only).
This text is also the docs view. --diff reports public contract changes;
a new take or copying parameter breaks callers that pass a resource.
--json prints structured contracts or changes. Both inputs must check.
Exit status: 0 on success, 1 on a source error, 2 on invalid arguments.
`

// inspectCommand dispatches checked machine, public API and Go reach queries.
func inspectCommand(args []string) error {
	if len(args) > 0 && args[0] == "api" {
		if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
			_, err := os.Stdout.WriteString(inspectAPIHelp)
			return err
		}
		return inspectAPICommand(args[1:])
	}
	if len(args) == 0 || args[0] != "machine" {
		return inspectGoCommand(args)
	}
	if rest := args[1:]; len(rest) == 1 && (rest[0] == "--help" || rest[0] == "-h") {
		_, err := os.Stdout.WriteString(inspectMachineHelp)
		return err
	}
	return inspectMachineCommand(args[1:])
}

func inspectAPICommand(args []string) error {
	asJSON, diff := false, false
	var paths []string
	for _, arg := range args {
		switch arg {
		case "--json":
			asJSON = true
		case "--diff":
			diff = true
		default:
			if strings.HasPrefix(arg, "-") {
				return invalidInvocation(fmt.Errorf("inspect api: unknown option %s", arg))
			}
			paths = append(paths, arg)
		}
	}
	if (!diff && len(paths) != 1) || (diff && len(paths) != 2) {
		return invalidInvocation(fmt.Errorf("inspect api: supply FILE|DIR, or --diff OLD NEW"))
	}
	var tables []string
	for _, path := range paths {
		opts, err := parseFileOptions("inspect api", []string{path})
		if err != nil {
			return invalidInvocation(err)
		}
		l, err := load(opts.file, opts.target)
		if err != nil {
			return err
		}
		if !l.result.Checked {
			return refuseUnchecked(l, opts)
		}
		t := codeTablesOf(l.analysis)
		// A single-file check has no file name in the syntax snapshot.
		if !isPackageFolder(path) {
			root := &t.Packages[len(t.Packages)-1]
			for i := range root.Contracts {
				for j := range root.Contracts[i].Copies {
					if c := &root.Contracts[i].Copies[j]; c.Where != "" {
						c.Where = path + ":" + c.Where
					}
				}
			}
		}
		b, err := json.Marshal(t)
		if err != nil {
			return exitStatusError{code: 1, message: "inspect api: " + err.Error()}
		}
		tables = append(tables, string(b))
	}
	// The view is std code's Api (ApiText, ApiDiff, ApiDiffText), linked
	// from internal/linked/code; --json reads its answer back into the
	// api types, so the JSON keeps their field names.
	var out []string
	var err error
	if diff {
		out, err = code.ApiDiff(context.Background(), tables[0], tables[1], asJSON)
	} else {
		out, err = code.Api(context.Background(), tables[0], asJSON)
	}
	if err != nil {
		return exitStatusError{code: 1, message: "inspect api: " + err.Error()}
	}
	if len(out) != 3 || out[0] != "0" {
		return exitStatusError{code: 1, message: "inspect api: " + strings.Join(out, " ")}
	}
	if !asJSON {
		_, err = os.Stdout.WriteString(out[1])
		return err
	}
	if !diff {
		var fs []api.Function
		if err := json.Unmarshal([]byte(out[1]), &fs); err != nil {
			return exitStatusError{code: 1, message: "inspect api: " + err.Error()}
		}
		return printJSON(fs)
	}
	type side struct {
		Tag   string        `json:"_tag"`
		Value *api.Function `json:"value"`
	}
	var changes []struct {
		Name                  string
		Old, New              side
		BreaksResourceCallers bool
	}
	if err := json.Unmarshal([]byte(out[1]), &changes); err != nil {
		return exitStatusError{code: 1, message: "inspect api: " + err.Error()}
	}
	list := []api.Change{}
	for _, c := range changes {
		list = append(list, api.Change{Name: c.Name, Old: c.Old.Value, New: c.New.Value, BreaksResourceCallers: c.BreaksResourceCallers})
	}
	return printJSON(list)
}

// inspectMachineCommand prints the machine graphs of the root package.
func inspectMachineCommand(args []string) error {
	opts, err := parseFileOptions("inspect machine", args)
	if err != nil {
		return invalidInvocation(err)
	}
	l, err := loadForPlatform(opts.file, opts.target, opts.platform)
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
