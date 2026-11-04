package main

import (
	"fmt"
	"path"
	"slices"
	"strings"
	"testing"
)

// The registry of reviewed scripts (reviewed.go) is checked here, apart from
// the cache coverage guard. The guard reads only what an entry declares; the
// checks below keep the declarations honest where a program can: no entry is
// stale, and a literal scan of each text finds no command that its entry
// does not declare. The scan is a cross-check of a reviewer's work, not the
// guard: it cannot see what the shell expands, which is why the guard does
// not read scripts at all.

// shellKeywords may start a shell command before the program it runs.
var shellKeywords = []string{"{", "}", "!", "if", "then", "else", "elif", "fi", "do", "done", "while", "until"}

// commandRunners are programs and shell builtins that run a command their
// arguments name.
var commandRunners = []string{"eval", "exec", "command", "builtin", ".", "source", "xargs", "env", "timeout", "nice", "nohup", "time", "trap"}

// registryProblems returns what is wrong with a registry of reviewed scripts
// for the given steps: two entries with one name or one text, an entry that
// no step runs (a stale entry), and an entry whose declaration does not match
// its text (declarationProblems).
func registryProblems(registry []*reviewedScript, steps []*Step) []string {
	var problems []string
	names, texts := map[string]bool{}, map[string]bool{}
	for _, script := range registry {
		if names[script.name] {
			problems = append(problems, fmt.Sprintf("two reviewed scripts are named %q", script.name))
		}
		if texts[script.text] {
			problems = append(problems, fmt.Sprintf("reviewed script %q repeats another entry's text", script.name))
		}
		names[script.name], texts[script.text] = true, true
		if !slices.ContainsFunc(steps, func(step *Step) bool { return runsScript(step.Argv, script.text) }) {
			problems = append(problems, fmt.Sprintf("no step runs reviewed script %q: a stale entry", script.name))
		}
		problems = append(problems, declarationProblems(script)...)
	}
	return problems
}

// runsScript reports whether argv runs sh -c text.
func runsScript(argv []string, text string) bool {
	for i := 0; i+2 < len(argv); i++ {
		if argv[i] == "sh" && argv[i+1] == "-c" && argv[i+2] == text {
			return true
		}
	}
	return false
}

// declarationProblems returns where a script's declared runs differ from the
// commands a literal scan of its text finds (scannedRuns), and each run that
// names a program for a literal program word or no program for an expanded
// one.
func declarationProblems(script *reviewedScript) []string {
	var problems []string
	scanned := scannedRuns(script.text)
	var declared [][]string
	for _, run := range script.runs {
		declared = append(declared, run.argv)
		switch {
		case len(run.argv) == 0:
			problems = append(problems, fmt.Sprintf("reviewed script %q declares an empty run", script.name))
		case literalWord(run.argv[0]) && run.program != "":
			problems = append(problems, fmt.Sprintf("reviewed script %q declares program %q for the literal program %q", script.name, run.program, run.argv[0]))
		case !literalWord(run.argv[0]) && run.program == "":
			problems = append(problems, fmt.Sprintf("reviewed script %q does not declare what %q runs", script.name, run.argv[0]))
		}
	}
	if !slices.EqualFunc(scanned, declared, slices.Equal) {
		problems = append(problems, fmt.Sprintf("reviewed script %q declares the runs %q, and its text has %q", script.name, declared, scanned))
	}
	return problems
}

// scannedRuns returns the commands of a script that its entry must declare:
// each simple command with a word that names go, whose program word is not
// literal, or whose program runs a command and is given a word that is not
// literal. A command is its words with the quotes removed, after leading
// assignments and keywords.
func scannedRuns(text string) [][]string {
	var runs [][]string
	for _, words := range scriptCommands(text) {
		for len(words) > 0 && (isAssignment(words[0]) || slices.Contains(shellKeywords, words[0])) {
			words = words[1:]
		}
		if len(words) == 0 {
			continue
		}
		runner := slices.Contains(commandRunners, words[0]) || slices.Contains(shells, path.Base(words[0]))
		expanded := func(w string) bool { return !literalWord(w) }
		if slices.ContainsFunc(words, func(w string) bool { return path.Base(w) == "go" }) ||
			expanded(words[0]) ||
			runner && slices.ContainsFunc(words[1:], expanded) {
			runs = append(runs, words)
		}
	}
	return runs
}

// literalWord reports whether the shell leaves a word as written: no
// parameter expansion, command substitution, escape or glob. A lone "[" is
// the test command.
func literalWord(word string) bool {
	if strings.ContainsAny(word, "$`\\*?~") {
		return false
	}
	open := strings.Index(word, "[")
	return open < 0 || !strings.Contains(word[open+1:], "]")
}

func isAssignment(word string) bool {
	name, _, ok := strings.Cut(word, "=")
	if !ok || name == "" || name[0] >= '0' && name[0] <= '9' {
		return false
	}
	return !strings.ContainsFunc(name, func(r rune) bool {
		return !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
	})
}

// scriptCommands splits a shell script into its simple commands, each a list
// of words. Every separator a command can hide behind ends a command, inside
// quotes too: a newline, ;, &, |, a parenthesis, $( and a backquote. Quotes
// are dropped and a backslash-newline joins lines. The split is coarse on
// purpose: a quoted go is read as a command, which can only ask for more
// declarations.
func scriptCommands(script string) [][]string {
	var commands [][]string
	var words []string
	var word strings.Builder
	endWord := func() {
		if word.Len() > 0 {
			words = append(words, word.String())
			word.Reset()
		}
	}
	endCommand := func() {
		endWord()
		if len(words) > 0 {
			commands = append(commands, words)
			words = nil
		}
	}
	for i := 0; i < len(script); i++ {
		switch c := script[i]; c {
		case '\\':
			if i+1 < len(script) && script[i+1] == '\n' {
				i++
				endWord()
			} else {
				word.WriteByte(c)
			}
		case '$':
			if i+1 < len(script) && script[i+1] == '(' {
				i++
				endCommand()
			} else {
				word.WriteByte(c)
			}
		case '"', '\'':
		case ' ', '\t':
			endWord()
		case '\n', ';', '&', '|', '(', ')', '`':
			endCommand()
		default:
			word.WriteByte(c)
		}
	}
	endCommand()
	return commands
}

// TestReviewedScripts checks the registry against the gate's steps, then
// shows that each check fails on the registry it must refuse: a stale entry,
// a repeated name or text, a run dropped from or added to a declaration, an
// expanded program without its declaration, and the round 2 (B1) forms
// registered with no declaration.
func TestReviewedScripts(t *testing.T) {
	t.Parallel()
	_, steps := repositoryCensus(t)
	if problems := registryProblems(reviewedScripts, steps); len(problems) > 0 {
		t.Fatalf("the reviewed scripts do not match the gate's steps:\n%s", strings.Join(problems, "\n"))
	}
	declaredRuns := 0
	for _, script := range reviewedScripts {
		declaredRuns += len(script.runs)
	}
	if declaredRuns == 0 {
		t.Fatal("no reviewed script declares a run; the cross-check is vacuous")
	}

	refuses := func(name string, registry []*reviewedScript, want string) {
		t.Helper()
		problems := registryProblems(registry, steps)
		if !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, want) }) {
			t.Errorf("%s: no problem says %q: %q", name, want, problems)
		}
	}
	with := func(entries ...*reviewedScript) []*reviewedScript {
		return append(slices.Clone(reviewedScripts), entries...)
	}
	refuses("an entry no step runs", with(&reviewedScript{name: "stale probe", text: "true"}), "a stale entry")
	refuses("a repeated name", with(&reviewedScript{name: gofmtScript.name, text: "true"}), "two reviewed scripts are named")
	refuses("a repeated text", with(&reviewedScript{name: "copy probe", text: gofmtScript.text}), "repeats another entry's text")

	replaced := func(script *reviewedScript, runs []scriptRun) []*reviewedScript {
		entry := *script
		entry.runs = runs
		registry := slices.Clone(reviewedScripts)
		registry[slices.Index(registry, script)] = &entry
		return registry
	}
	for _, script := range reviewedScripts {
		for i := range script.runs {
			refuses(fmt.Sprintf("%s without run %d", script.name, i), replaced(script, slices.Delete(slices.Clone(script.runs), i, i+1)), "declares the runs")
		}
		refuses(script.name+" with an added run", replaced(script, append(slices.Clone(script.runs), scriptRun{argv: []string{"go", "vet", "./..."}})), "declares the runs")
	}
	bin := slices.IndexFunc(provenanceScript.runs, func(run scriptRun) bool { return run.program != "" })
	if bin < 0 {
		t.Fatal("the provenance script declares no expanded program")
	}
	undeclared := slices.Clone(provenanceScript.runs)
	undeclared[bin].program = ""
	refuses("$bin without its program", replaced(provenanceScript, undeclared), `does not declare what "$bin" runs`)
	misdeclared := slices.Clone(provenanceScript.runs)
	misdeclared[0].program = "probe"
	refuses("a program for a literal go", replaced(provenanceScript, misdeclared), "declares program")

	b1 := "go test -C tools/provenance ./..."
	for _, text := range []string{
		"${GO:-go} -C tools/provenance test ./...",
		"$GO -C tools/provenance test ./...",
		`X="` + b1 + `"; $X`,
		"$X",
		`eval "$X"`,
		"/usr/local/bin/g[o] -C tools/provenance test ./...",
		`"$1"o -C tools/provenance test ./...`,
		"`echo go` test ./...",
		"cd . && " + b1,
		"echo | xargs " + b1,
	} {
		script := &reviewedScript{name: "undeclared probe", text: text}
		if problems := declarationProblems(script); len(problems) == 0 {
			t.Errorf("a script %q registered with no runs passes the cross-check", text)
		}
	}
}
