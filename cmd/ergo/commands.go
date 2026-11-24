package main

import (
	"fmt"
	"os"
	"strings"
)

// fate is what a command word does in v1. Every word the CLI has ever
// accepted is in the command table with one fate, so a word cannot be
// classified twice or forgotten.
type fate int

const (
	// available commands run on the one front end.
	available fate = iota
	// removed commands were removed in v1.
	removed
	// dark commands are unavailable until they are ported to the new
	// front end.
	dark
)

// cliCommand is one row of the command table.
type cliCommand struct {
	// name is the command word, or a word and a subcommand ("lint rules").
	name string
	fate fate
	// usage is the help line of an available command.
	usage string
	// help is the text of `ergo NAME --help`; empty means the general help.
	help string
	// reason follows "removed in v1;".
	reason string
	run    func(args []string) error
}

const toolingReason = "semantic tooling returns on the new front end"

// commands is the command table. Dispatch, the general help and the stubs
// read only this table.
var commands = []cliCommand{
	{name: "check", usage: "check FILE|DIR|DIR/... [FILE|DIR|DIR/...]... [--seed N] [--eval-steps N] [--eval-bytes N] [--strict] [--target go] | check --want FIXTURE|DIR...", run: checkCommand},
	{name: "diagnostics", usage: "diagnostics FILE|DIR [--lint-config FILE] [--rules MANIFEST]... [--strict] [--json] [--target go]", help: diagnosticsHelp, run: diagnosticsCommand},
	{name: "build", usage: "build FILE|DIR [-o PATH] [-work] [--emitted] [--locked] [-rewrites=on|proved|off|canary[=N]] [--debug] [--go-trace] [--release] [--platform OS/ARCH[/LEVEL]] [--target go]", run: buildCommand},
	{name: "run", usage: "run FILE|DIR [--] [ARGS...]", help: runHelp, run: runCommand},
	{name: "fix", usage: "fix [--check|--dry-run] [--json] [--std DIR] [--go] PATH...", help: fixHelp, run: fixCommand},
	{name: "fmt", usage: "fmt FILE...|DIR/... [--check] [--json] | fmt --stdin", help: fmtHelp, run: formatCommand},
	{name: "test", usage: "test FILE|DIR|DIR/... [--seed N] [--states N] [--law NAME] [--skip-law NAME] [--run NAME] [--receipt FILE] [--explore [dfs|pct|random] [--preempt B] [--depth D] [--schedules S]] [--schedule TOKEN] [-work] [--emitted] [--affected [BASE] [--list] [--why]] [--go-trace] [--json] [--target go] | test DIR --cases CASES [--locked]", run: testCommand},
	{name: "bind", usage: "bind go [-o DIR] PACKAGE...", help: bindHelp, run: bindCommand},
	{name: "get", usage: "get MODULE[@VERSION]...", help: getHelp, run: getCommand},
	{name: "lib", usage: "lib FILE|DIR -o DIR", help: libHelp, run: libCommand},
	{name: "go", usage: "go VERB [ARGS...]", help: goHelp, run: ergoGoCommand},
	{name: "gen", usage: "gen rpc|cli [--check] DIR.OPTYPE", help: genHelp, run: genCommand},
	{name: "dev", usage: "dev [DIR] [--idle DURATION]", help: devHelp, run: devCommand},
	{name: "task", usage: "task --list [--json] [DIR] | task gen [--check] [--fresh] [--jobs N] [NAME...] [DIR]", help: taskHelp, run: taskCommand},
	{name: "budget", usage: "budget [--config FILE] [--width N] [--memory SIZE] [--class crit|lane] [--lane NAME] -- COMMAND [ARGS...]", help: budgetHelp, run: budgetCommand},
	{name: "work", usage: "work COMMAND [ARGS...]", help: workHelp, run: workCommand},
	{name: "code", usage: "code [--in DIR] [--json] COMMAND [ARGS...]", help: codeHelp, run: codeCommand},
	{name: "graph", usage: "graph [--in DIR|DIR/...] export|cycles|why|affected|hidden|chains|fan|interfaces|files|directories|field-writes|test-support|seams|folder-cycles|layers [--json]", help: graphHelp, run: graphCommand},
	{name: "version", usage: "version [--json]", help: versionHelp, run: versionCommand},
	{name: "conform", usage: conformUsage, help: conformHelp, run: conformCommand},
	{name: "tsmap", usage: "tsmap [--emit-js FILE.ergo]", help: tsmapHelp, run: tsmapCommand},
	{name: "edition", fate: removed, reason: "there is one language and no editions"},
	{name: "lint", usage: "lint FILE|DIR [--preset P]... [--lint-config FILE] [--rules MANIFEST]... [--strict] [--json] [--target go]", help: lintHelp, run: lintCommand},
	{name: "lint rules", usage: "lint rules [--preset P]... [--lint-config FILE] [--rules MANIFEST]...", help: lintRulesHelp, run: lintRulesCommand},
	{name: "lint test", usage: "lint test PATH... [--update] (--lint-config FILE | --rules MANIFEST...)", help: lintTestHelp, run: lintTestCommand},
	{name: "lint receipt", fate: removed, reason: "cost receipts return with rule packs"},
	{name: "query", fate: removed, reason: toolingReason},
	{name: "type", fate: removed, reason: toolingReason},
	{name: "inspect", usage: "inspect FILE|DIR [--target go] | inspect machine FILE|DIR [--json] | inspect deps DIR [--json] | inspect facts FILE|DIR [--json] | inspect forms FILE|DIR [--json] | inspect rewrites FILE|DIR [--json]", help: inspectHelp, run: inspectCommand},
	{name: "explain", fate: removed, reason: toolingReason},
	{name: "lsp", fate: dark},
	{name: "mcp", fate: dark},
}

// lookupCommand finds the row of args: a word and its subcommand first,
// then the word alone.
func lookupCommand(args []string) (cliCommand, bool) {
	var names []string
	if len(args) >= 2 {
		names = append(names, args[0]+" "+args[1])
	}
	names = append(names, args[0])
	for _, name := range names {
		for _, c := range commands {
			if c.name == name {
				return c, true
			}
		}
	}
	return cliCommand{}, false
}

// stubMessage is the one line a command that does not run prints.
func (c cliCommand) stubMessage() string {
	switch c.fate {
	case removed:
		return fmt.Sprintf("ergo %s: removed in v1; %s", c.name, c.reason)
	case dark:
		return fmt.Sprintf("ergo %s: unavailable until ported to the new front end", c.name)
	}
	return ""
}

// generalHelp lists the available commands. It never names a removed or
// dark command.
func generalHelp() string {
	var b strings.Builder
	b.WriteString("Ergo prototype\nusage: ergo COMMAND ...\n\n")
	for _, c := range commands {
		if c.fate == available {
			fmt.Fprintf(&b, "  ergo %s\n", c.usage)
		}
	}
	b.WriteString("\nGo is the only target; --target accepts only go.\nRun ergo COMMAND --help for one command's options and exit codes.\n")
	return b.String()
}

// command dispatches one invocation through the command table. A command
// that does not run prints its stub before any argument is read.
func command(args []string) error {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "help" || args[0] == "-h")) {
		fmt.Print(generalHelp())
		return nil
	}
	c, ok := lookupCommand(args)
	if !ok {
		return fmt.Errorf("unknown command %s; use ergo --help", args[0])
	}
	if c.fate != available {
		return exitStatusError{code: 1, message: c.stubMessage()}
	}
	rest := args[1:]
	if strings.Contains(c.name, " ") {
		rest = args[2:]
	}
	if len(rest) == 1 && (rest[0] == "--help" || rest[0] == "-h") {
		help := c.help
		if help == "" {
			help = generalHelp()
		}
		fmt.Fprint(os.Stdout, help)
		return nil
	}
	return c.run(rest)
}
