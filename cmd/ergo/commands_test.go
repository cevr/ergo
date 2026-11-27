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
			if c.run == nil || c.usage == "" || c.reason != "" {
				t.Fatalf("available %q: %+v", c.name, c)
			}
			if !c.hidden {
				if !strings.Contains(help, "ergo "+c.usage+"\n") {
					t.Fatalf("help does not list %q", c.usage)
				}
			} else if strings.Contains(help, "  ergo "+strings.Fields(c.name)[0]+" ") {
				t.Fatalf("help should not list hidden command %q", c.name)
			}
		case removed, dark:
			if c.run != nil || c.help != "" || c.usage != "" || (c.fate == dark) != (c.reason == "") {
				t.Fatalf("stub %q: %+v", c.name, c)
			}
			if regexp.MustCompile(`\b` + regexp.QuoteMeta(c.name) + `\b`).MatchString(help) {
				t.Fatalf("help names the %s command %q", map[fate]string{removed: "removed", dark: "dark"}[c.fate], c.name)
			}
		default:
			t.Fatalf("%q has no fate", c.name)
		}
	}
	for _, kept := range []string{"check", "build", "run", "fmt", "test", "fix", "lint", "verify", "inspect"} {
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
// and why, nothing on stdout, exit 1.
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
			stdout, stderr, code := runSmokeCLI(t, binary, dir, "", argv...)
			if code != 1 || len(stdout) != 0 || string(stderr) != c.stubMessage()+"\n" {
				t.Fatalf("ergo %s: exit %d stdout %q stderr %q; want exit 1 and %q", strings.Join(argv, " "), code, stdout, stderr, c.stubMessage())
			}
		}
	}
	// Each stub is "ergo NAME: " and the text the spec gives the command.
	want := map[string]string{
		"edition":      "removed in v1; there is one language and no editions",
		"lint receipt": "removed in v1; cost receipts return with rule packs",
		"explain":      "removed in v1; semantic tooling returns on the new front end",
		"lsp":          "unavailable until ported to the new front end",
		"mcp":          "unavailable until ported to the new front end",
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
	for _, args := range [][]string{nil, {"--help"}, {"-h"}, {"help"}} {
		stdout, stderr, code := runSmokeCLI(t, binary, dir, "", args...)
		if code != 0 || len(stderr) != 0 || string(stdout) != generalHelp() {
			t.Fatalf("ergo %v: exit %d stdout %q stderr %q", args, code, stdout, stderr)
		}
	}
	for _, word := range []string{"edition", "query", "explain", "lsp", "mcp", "ergo type", "Ergo prototype", "Go is the only target", "--target go", "--native", "--compiled", "budget", "tsmap"} {
		if strings.Contains(generalHelp(), word) {
			t.Fatalf("help names forbidden term %q", word)
		}
	}
	for _, section := range []string{"Examples:", "Core:", "Verify & Inspect:", "Packages & Workspace:"} {
		if !strings.Contains(generalHelp(), section) {
			t.Fatalf("generalHelp lacks grouped section %q", section)
		}
	}
	for args, want := range map[string]string{
		"check --help":         checkHelp,
		"build -h":             buildHelp,
		"help test":            testHelp,
		"verify --help":        verifyHelp,
		"inspect --help":       inspectHelp,
		"diagnostics --help":   diagnosticsHelp,
		"fmt --help":           fmtHelp,
		"fmt -h":               fmtHelp,
		"fix --help":           fixHelp,
		"--help-all":           generalHelpAll(),
		"test --help-all":      testAllHelp,
		"build --help-all":     buildAllHelp,
		"fix --help-all":       fixAllHelp,
		"inspect graph --help": graphHelp,
	} {
		stdout, _, code := runSmokeCLI(t, binary, dir, "", strings.Fields(args)...)
		if code != 0 || string(stdout) != want {
			t.Fatalf("ergo %s: exit %d stdout %q", args, code, stdout)
		}
	}
	stdout, stderr, code := runSmokeCLI(t, binary, dir, "", "frobnicate", "main.ergo")
	if code != 1 || len(stdout) != 0 || string(stderr) != "unknown command frobnicate; use ergo --help\n" {
		t.Fatalf("an unknown command: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	for _, usage := range []string{"check [PATH...]", "build [PATH]", "run [PATH]", "fmt [PATH...]", "test [PATH...]", "fix [--check]", "verify mutants|explore|conform|cases", "inspect [api|machine|deps|facts|copies|forms|rewrites|graph]"} {
		if !slices.ContainsFunc(strings.Split(generalHelp(), "\n"), func(line string) bool { return strings.HasPrefix(line, "  ergo "+usage) }) {
			t.Fatalf("help lacks %q", usage)
		}
	}
}
