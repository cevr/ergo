package main

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

// TestCommandTableClassifiesEveryWordOnce iterates the command table: names
// are unique, each row has the fields its fate needs and no others, and the
// general help lists exactly the available rows.
func TestCommandTableClassifiesEveryWordOnce(t *testing.T) {
	seen := map[string]bool{}
	help := generalHelp()
	ticket := regexp.MustCompile(`^#[0-9]+$`)
	for _, c := range commands {
		if seen[c.name] {
			t.Fatalf("%q is classified twice", c.name)
		}
		seen[c.name] = true
		if words := strings.Fields(c.name); len(words) == 2 && !seen[words[0]] {
			t.Fatalf("subcommand %q precedes its command", c.name)
		}
		switch c.fate {
		case available:
			if c.run == nil || c.usage == "" || c.ticket != "" || c.reason != "" {
				t.Fatalf("available %q: %+v", c.name, c)
			}
			if !strings.Contains(help, "ergo "+c.usage+"\n") {
				t.Fatalf("help does not list %q", c.usage)
			}
		case removed, dark:
			if c.run != nil || c.help != "" || c.usage != "" || !ticket.MatchString(c.ticket) || (c.fate == dark) != (c.reason == "") {
				t.Fatalf("stub %q: %+v", c.name, c)
			}
			if regexp.MustCompile(`\b` + regexp.QuoteMeta(strings.Fields(c.name)[0]) + `\b`).MatchString(help) {
				t.Fatalf("help names the %s command %q", map[fate]string{removed: "removed", dark: "dark"}[c.fate], c.name)
			}
		default:
			t.Fatalf("%q has no fate", c.name)
		}
	}
	for _, kept := range []string{"check", "diagnostics", "build", "run", "fmt", "test"} {
		if row, ok := lookupCommand([]string{kept}); !ok || row.fate != available {
			t.Fatalf("%s is not kept", kept)
		}
	}
	if _, ok := lookupCommand([]string{"frobnicate"}); ok {
		t.Fatal("an unknown word was found")
	}
	if row, _ := lookupCommand([]string{"lint", "rules", "x"}); row.name != "lint rules" {
		t.Fatalf("lint rules resolved to %q", row.name)
	}
	if row, _ := lookupCommand([]string{"lint", "main.ergo"}); row.name != "lint" {
		t.Fatalf("lint FILE resolved to %q", row.name)
	}
}

// TestStubsAnswerBeforeReadingArguments drives every command that does not
// run, under every argument shape: one line on stderr naming the command
// and its ticket, nothing on stdout, exit 1.
func TestStubsAnswerBeforeReadingArguments(t *testing.T) {
	binary := buildTestCLI(t)
	dir := t.TempDir()
	smokeWrite(t, dir, "main.ergo", sketchCleanSource)
	for _, c := range commands {
		if c.fate == available {
			continue
		}
		for _, args := range [][]string{nil, {"main.ergo"}, {"--help"}, {"-h"}, {"main.ergo", "--json"}, {"a", "b", "c"}, {"--target", "go"}, {"/nonexistent/loop-probe-x"}} {
			argv := append(strings.Fields(c.name), args...)
			stdout, stderr, code := runSmokeCLI(t, processDeadline, binary, dir, "", argv...)
			if code != 1 || len(stdout) != 0 || string(stderr) != c.stubMessage()+"\n" {
				t.Fatalf("ergo %s: exit %d stdout %q stderr %q; want exit 1 and %q", strings.Join(argv, " "), code, stdout, stderr, c.stubMessage())
			}
		}
	}
	// Each stub is "ergo NAME: " and the text the spec gives the command.
	want := map[string]string{
		"edition":      "removed in v1; there is one language and no editions (#205)",
		"lint receipt": "removed in v1; linting returns on the new checker (#203)",
		"graph":        "removed in v1; semantic tooling returns on the new front end (#203)",
		"lsp":          "unavailable until ported to the new front end (#203)",
		"mcp":          "unavailable until ported to the new front end (#203)",
	}
	for name, text := range want {
		row, _ := lookupCommand(strings.Fields(name))
		if message := "ergo " + name + ": " + text; row.stubMessage() != message {
			t.Fatalf("%s: %q, want %q", name, row.stubMessage(), message)
		}
	}
}

func TestHelpListsOnlyTheKeptCommands(t *testing.T) {
	binary := buildTestCLI(t)
	dir := t.TempDir()
	for _, args := range [][]string{nil, {"--help"}, {"-h"}, {"help"}, {"check", "--help"}, {"build", "-h"}} {
		stdout, stderr, code := runSmokeCLI(t, processDeadline, binary, dir, "", args...)
		if code != 0 || len(stderr) != 0 || string(stdout) != generalHelp() {
			t.Fatalf("ergo %v: exit %d stdout %q stderr %q", args, code, stdout, stderr)
		}
	}
	for _, word := range []string{"edition", "lint", "query", "graph", "inspect", "explain", "lsp", "mcp", "ergo type"} {
		if regexp.MustCompile(`\b` + word + `\b`).MatchString(generalHelp()) {
			t.Fatalf("help names %q", word)
		}
	}
	for args, want := range map[string]string{"diagnostics --help": diagnosticsHelp, "fmt --help": fmtHelp, "fmt -h": fmtHelp} {
		stdout, _, code := runSmokeCLI(t, processDeadline, binary, dir, "", strings.Fields(args)...)
		if code != 0 || string(stdout) != want {
			t.Fatalf("ergo %s: exit %d stdout %q", args, code, stdout)
		}
	}
	stdout, stderr, code := runSmokeCLI(t, processDeadline, binary, dir, "", "frobnicate", "main.ergo")
	if code != 1 || len(stdout) != 0 || string(stderr) != "unknown command frobnicate; use ergo --help\n" {
		t.Fatalf("an unknown command: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	for _, usage := range []string{"check FILE", "diagnostics FILE", "build FILE", "run FILE", "fmt FILE...", "test FILE"} {
		if !slices.ContainsFunc(strings.Split(generalHelp(), "\n"), func(line string) bool { return strings.HasPrefix(line, "  ergo "+usage) }) {
			t.Fatalf("help lacks %q", usage)
		}
	}
}
