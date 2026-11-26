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
	// group places an available command in generalHelp ("core", "verify", "workspace").
	group string
	// hidden keeps deprecated/plumbing commands runnable without advertising them in --help.
	hidden bool
	// usage is the help line of an available command.
	usage string
	// help is the text of `ergo NAME --help`; empty means the general help.
	help string
	// allHelp is the text of `ergo NAME --help-all`; empty means help.
	allHelp string
	// reason follows "removed in v1;".
	reason string
	run    func(args []string) error
}

const toolingReason = "semantic tooling returns on the new front end"

// commands is the command table. Dispatch, the general help and the stubs
// read only this table.
var commands = []cliCommand{
	{name: "check", group: "core", usage: "check [PATH...] [--strict] [--json] [-j N] [--fresh] | check --want FIXTURE|DIR...", help: checkHelp, allHelp: checkAllHelp, run: checkCommand},
	{name: "test", group: "core", usage: "test [PATH...] [-k PATTERN] [--skip PATTERN] [--fresh] [-j N] [--seed N] [--json]", help: testHelp, allHelp: testAllHelp, run: testCommand},
	{name: "build", group: "core", usage: "build [PATH] [-o PATH] [--lib -o DIR] [--locked] [--debug] [--release] [--platform OS/ARCH]", help: buildHelp, allHelp: buildAllHelp, run: buildCommand},
	{name: "run", group: "core", usage: "run [PATH] [--locked] [--debug] [--json] [--] [ARGS...]", help: runHelp, allHelp: runAllHelp, run: runCommand},
	{name: "fmt", group: "core", usage: "fmt [PATH...] [--check] [--json] | fmt --stdin", help: fmtHelp, run: formatCommand},
	{name: "lint", group: "core", usage: "lint [PATH...] [--preset P]... [--lint-config FILE] [--rules MANIFEST]... [--gate P] [--diff[=REV]] [--strict] [--json]", help: lintHelp, run: lintCommand},
	{name: "lint rules", group: "core", usage: "lint rules [--preset P]... [--lint-config FILE] [--rules MANIFEST]...", help: lintRulesHelp, run: lintRulesCommand},
	{name: "lint test", group: "core", usage: "lint test PATH... [--update] (--lint-config FILE | --rules MANIFEST...)", help: lintTestHelp, run: lintTestCommand},
	{name: "fix", group: "core", usage: "fix [--check] [--json] [PATH...]", help: fixHelp, allHelp: fixAllHelp, run: fixCommand},
	{name: "verify", group: "verify", usage: "verify mutants|explore|conform|cases ...", help: verifyHelp, run: verifyCommand},
	{name: "inspect", group: "verify", usage: "inspect [api|machine|deps|facts|copies|forms|rewrites|graph] ...", help: inspectHelp, run: inspectCommand},
	{name: "code", group: "verify", usage: "code [--in DIR] [--json] COMMAND [ARGS...]", help: codeHelp, run: codeCommand},
	{name: "dev", group: "verify", usage: "dev [DIR] [--idle DURATION]", help: devHelp, run: devCommand},
	{name: "gen", group: "workspace", usage: "gen rpc|cli [--check] DIR.OPTYPE", help: genHelp, run: genCommand},
	{name: "task", group: "workspace", usage: "task --list [--json] [DIR] | task gen [--check] [--fresh] [--jobs N] [NAME...] [DIR]", help: taskHelp, run: taskCommand},
	{name: "get", group: "workspace", usage: "get MODULE[@VERSION]...", help: getHelp, run: getCommand},
	{name: "bind", group: "workspace", usage: "bind go [-o DIR] PACKAGE...", help: bindHelp, run: bindCommand},
	{name: "work", group: "workspace", usage: "work COMMAND [ARGS...]", help: workHelp, run: workCommand},
	{name: "version", group: "workspace", usage: "version [--json]", help: versionHelp, run: versionCommand},
	{name: "go", group: "workspace", usage: "go VERB [ARGS...]", help: goHelp, run: ergoGoCommand},
	{name: "diagnostics", hidden: true, usage: "diagnostics [PATH] [--lint-config FILE] [--rules MANIFEST]... [--strict] [--json]", help: diagnosticsHelp, run: diagnosticsCommand},
	{name: "lib", hidden: true, usage: "lib [PATH] -o DIR", help: libHelp, run: libCommand},
	{name: "graph", hidden: true, usage: "graph [--in DIR|DIR/...] export|cycles|why|affected|hidden|chains|fan|interfaces|files|directories|field-writes|test-support|seams|folder-cycles|tangles|compare|layers [--json]", help: graphHelp, run: graphCommand},
	{name: "conform", hidden: true, usage: conformUsage, help: conformHelp, run: conformCommand},
	{name: "budget", hidden: true, usage: "budget [--config FILE] [--width N] [--memory SIZE] [--class crit|lane] [--lane NAME] -- COMMAND [ARGS...]", help: budgetHelp, run: budgetCommand},
	{name: "tsmap", hidden: true, usage: "tsmap [--emit-js FILE.ergo]", help: tsmapHelp, run: tsmapCommand},
	{name: "edition", fate: removed, reason: "there is one language and no editions"},
	{name: "lint receipt", fate: removed, reason: "cost receipts return with rule packs"},
	{name: "query", fate: removed, reason: toolingReason},
	{name: "type", fate: removed, reason: toolingReason},
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

const helpBanner = "ergo — failures handled, services supplied, laws tested"

var helpGroups = []struct {
	key   string
	title string
}{
	{key: "core", title: "Core:"},
	{key: "verify", title: "Verify & Inspect:"},
	{key: "workspace", title: "Packages & Workspace:"},
}

func writeHelpGroup(b *strings.Builder, key, title string) {
	b.WriteString(title + "\n")
	for _, c := range commands {
		if c.fate == available && !c.hidden && c.group == key {
			fmt.Fprintf(b, "  ergo %s\n", c.usage)
		}
	}
	b.WriteByte('\n')
}

// generalHelp lists the grouped primary commands. It never names a removed,
// dark, or hidden compatibility/plumbing command.
func generalHelp() string {
	var b strings.Builder
	b.WriteString(helpBanner + "\n")
	b.WriteString("usage: ergo COMMAND [OPTIONS] [PATH...]\n\n")
	b.WriteString("Examples:\n")
	b.WriteString("  ergo check                       Check current package or module\n")
	b.WriteString("  ergo test                        Run tests and laws in parallel\n")
	b.WriteString("  ergo test -k Parse --fresh       Run matching tests bypassing caches\n")
	b.WriteString("  ergo run examples/lookup         Build and run a program\n")
	b.WriteString("  ergo verify mutants              Run mutation checks on the project\n\n")
	for _, g := range helpGroups {
		writeHelpGroup(&b, g.key, g.title)
	}
	b.WriteString("Run ergo COMMAND --help for command options and exit codes.\n")
	b.WriteString("Run ergo --help-all for advanced options and compatibility aliases.\n")
	return b.String()
}

// generalHelpAll returns generalHelp followed by hidden/plumbing commands.
func generalHelpAll() string {
	var b strings.Builder
	b.WriteString(generalHelp())
	b.WriteString("\nPlumbing & compatibility commands:\n")
	for _, c := range commands {
		if c.fate == available && c.hidden {
			fmt.Fprintf(&b, "  ergo %s\n", c.usage)
		}
	}
	return b.String()
}

// command dispatches one invocation through the command table. A command
// that does not run prints its stub before any argument is read.
func command(args []string) error {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "help" || args[0] == "-h")) {
		fmt.Print(generalHelp())
		return nil
	}
	if len(args) == 1 && args[0] == "--help-all" {
		fmt.Print(generalHelpAll())
		return nil
	}
	if len(args) >= 2 && args[0] == "help" {
		return command(append(args[1:len(args):len(args)], "--help"))
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
	if len(rest) == 1 && rest[0] == "--help-all" {
		help := c.allHelp
		if help == "" {
			help = c.help
		}
		if help == "" {
			help = generalHelpAll()
		}
		fmt.Fprint(os.Stdout, help)
		return nil
	}
	return c.run(rest)
}
