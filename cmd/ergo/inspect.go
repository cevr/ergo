package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"ergo.local/prototype/internal/linked/code"

	"ergo.local/prototype/internal/sketch/api"
	"ergo.local/prototype/internal/sketchfront"
)

const inspectMachineHelp = `usage: ergo inspect machine FILE|DIR [--json]

Draws the graph of each machine of the root package (runtime.md, Machines
and actors 4 and 15): its states and signals, each arm of its step with
the state and signal variants it takes and the decisions it can give
(kind, target state, position), the timer and the work of each state, and
the pairs a run can reach with the arm that steps each. --json prints
each machine's view as JSON: std code's MachineView, the graph and its
reachable pairs (docs/spec/code.md).

Exit status: 0 when the package checks, 1 when it does not, 2 on an
invalid invocation.
` + "\n" + inspectAPIHelp + "\n" + inspectFactsHelp + "\n" + inspectCopiesHelp

const inspectAPIHelp = `usage: ergo inspect api FILE|DIR [--json]
       ergo inspect api --diff OLD NEW [--json]

Shows exported functions and methods from checked source: fails, needs,
takes (written or inferred), and generic copy sites (plain data only).
This text is also the docs view. --diff reports public contract changes;
a new take or copying parameter breaks callers that pass a resource.
--json prints structured contracts or changes. Both inputs must check.
Exit status: 0 on success, 1 on a source error, 2 on invalid arguments.
`

const inspectFactsHelp = `usage: ergo inspect facts FILE|DIR [--json]

Lists the facts the run build exports for Go's optimizer (facts.md,
Lowering 7): the range of each refined parameter, each index and cut a
dominating compare proves, each two borrow parameters that never overlap,
and each pure function, with its position, function, condition, evidence
(proved or checked) and reason. A tested fact is never exported. A test
build exports none: it checks each one. --json prints them as JSON.

Exit status: 0 when the package checks, 1 when it does not, 2 on an
invalid invocation.
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
	if len(args) > 0 && args[0] == "deps" {
		return inspectDepsCommand(args[1:])
	}
	if len(args) > 0 && args[0] == "facts" {
		if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
			_, err := os.Stdout.WriteString(inspectFactsHelp)
			return err
		}
		return inspectFactsCommand(args[1:])
	}
	if len(args) > 0 && args[0] == "copies" {
		return inspectCopiesCommand(args[1:])
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
				return unknownOption("inspect api", arg)
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

// inspectFactsCommand prints the facts the run build of the root package
// exports (sketchfront.InspectFacts).
func inspectFactsCommand(args []string) error {
	asJSON := false
	var rest []string
	for _, arg := range args {
		if arg == "--json" {
			asJSON = true
			continue
		}
		rest = append(rest, arg)
	}
	opts, err := parseFileOptions("inspect facts", rest)
	if err != nil {
		return invalidInvocation(err)
	}
	l, err := loadForPlatform(opts.file, opts.target, opts.platform)
	if err != nil {
		return err
	}
	if !l.result.Checked || l.analysis.Checked() == nil {
		return refuseUnchecked(l, opts)
	}
	facts, err := sketchfront.InspectFacts(l.analysis, l.source.Admitted())
	if err != nil {
		return err
	}
	if asJSON {
		return printJSON(facts)
	}
	_, err = os.Stdout.WriteString(sketchfront.FactsText(facts))
	return err
}

// inspectMachineCommand prints the machine graphs of the root package:
// a view of std code's MachineOf and MachineText, linked from
// internal/linked/code.
func inspectMachineCommand(args []string) error {
	opts, err := parseFileOptions("inspect machine", args)
	if err != nil {
		return invalidInvocation(err)
	}
	l, err := loadForPlatform(opts.file, opts.target, opts.platform)
	if err != nil {
		return err
	}
	if l.analysis.Root().Package == nil {
		return fmt.Errorf("inspect machine: %s does not check; run ergo check", opts.file)
	}
	t := codeTablesOf(l.analysis)
	// A single-file check has no file name in the syntax snapshot.
	name := l.source.String()
	fix := func(p *codePlace) {
		if p.File == "" {
			p.File = name
		}
	}
	for i := range t.Packages {
		for j := range t.Packages[i].Machines {
			m := &t.Packages[i].Machines[j]
			for k := range m.Arms {
				fix(&m.Arms[k].At)
				for e := range m.Arms[k].Edges {
					fix(&m.Arms[k].Edges[e].At)
				}
			}
			for k := range m.Timers {
				fix(&m.Timers[k].At)
			}
			for k := range m.Work {
				fix(&m.Work[k])
			}
		}
	}
	tables, err := json.Marshal(t)
	if err != nil {
		return exitStatusError{code: 1, message: "inspect machine: " + err.Error()}
	}
	out, err := code.Machines(context.Background(), string(tables), opts.json)
	if err != nil {
		return exitStatusError{code: 1, message: "inspect machine: " + err.Error()}
	}
	if len(out) != 3 || out[0] != "0" {
		return exitStatusError{code: 1, message: "inspect machine: " + strings.Join(out, " ")}
	}
	// With --json the answer is std code's []MachineView.
	_, err = os.Stdout.WriteString(out[1])
	return err
}
