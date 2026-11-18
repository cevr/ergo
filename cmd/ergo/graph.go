package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"ergo.local/prototype/internal/sketchfront"
)

const graphHelp = `usage: ergo graph export [DIR|DIR/...] [--json]

ergo graph prints the graph of the program in DIR (the current folder by
default), or of every package below DIR with DIR/..., as the checker's
rows (docs/spec/graph.md): node rows (modules, packages, files,
declarations, tasks) and edge rows (contains, imports, uses, needs, fails,
provides, reexports, testof, lawof, input), each by a dense id. std
codegraph decodes the same rows. With --json the rows are JSON; without,
one line per row.

A package that does not check gives the rows the checker resolved and no
tasks.

Exit status: 0 printed; 1 the program cannot be read; 2 invalid invocation.
`

// graphCommand is `ergo graph`.
func graphCommand(args []string) error {
	if len(args) == 0 || args[0] != "export" {
		return invalidInvocation(fmt.Errorf("ergo graph: supply a command: export"))
	}
	jsonOut := false
	var dirs []string
	for _, arg := range args[1:] {
		switch {
		case arg == "--json":
			jsonOut = true
		case strings.HasPrefix(arg, "-"):
			return invalidInvocation(fmt.Errorf("ergo graph export: unknown option %s", arg))
		default:
			dirs = append(dirs, arg)
		}
	}
	dir := "."
	switch len(dirs) {
	case 0:
	case 1:
		dir = dirs[0]
	default:
		return invalidInvocation(fmt.Errorf("ergo graph export: supply one folder, DIR or DIR/..."))
	}
	var as []sketchfront.Analysis
	if recursivePattern(dir) {
		var err error
		as, err = sketchfront.CheckTree(strings.TrimSuffix(strings.TrimSuffix(dir, "..."), "/"))
		if err != nil {
			return exitStatusError{code: 1, message: "ergo graph: " + err.Error()}
		}
	} else {
		a, err := sketchfront.CheckDir(dir)
		if err != nil {
			return exitStatusError{code: 1, message: "ergo graph: " + err.Error()}
		}
		as = append(as, a)
	}
	t := sketchfront.GraphOf(as)
	if !jsonOut {
		_, err := os.Stdout.WriteString(t.Text())
		return err
	}
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(append(data, '\n'))
	return err
}
