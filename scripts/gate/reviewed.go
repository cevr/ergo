package main

// A gate step may run a shell script (sh -c SCRIPT) only when the script is
// one of reviewedScripts, byte for byte. No program can read what a shell
// script runs: a parameter expansion, a glob or eval can name go in a form no
// reader sees (gate-testcache-gap rounds 1 and 2). So each script here
// declares, by hand, the commands it runs that a cache coverage check must
// see, and a reviewer checks the declaration against the text.
// TestTestCommandsHaveCacheCoverage reads the declaration, never the script:
// it admits a sh -c step only when its script is registered here, and refuses
// every other script, whatever it contains. TestReviewedScripts checks that
// every entry is used by a step and that a literal scan of each text finds
// no go command and no expanded program that the entry does not declare.
//
// The steps take their scripts from these values, so a step and its entry
// cannot differ. To add or change a script, change its entry here, declare
// what it runs, and have it reviewed.
type reviewedScript struct {
	// name says what the script does.
	name string
	// text is the script, as the step gives it to sh -c.
	text string
	// runs are the script's commands that start a Go tool, and the ones
	// whose program word the shell expands ("$bin"), in the order the text
	// writes them. Each is the command's words with the quotes removed.
	// Every other command of the script starts a program with a literal
	// name that is not go (git, gofmt, mkdir, cmp, mv, echo).
	runs []scriptRun
}

// scriptRun is one command a reviewed script runs.
type scriptRun struct {
	argv []string
	// program is the program argv[0] names when the shell expands it, such
	// as "$bin"; empty when argv[0] is a literal program name.
	program string
}

// command returns the step argv that runs the script: sh -c, the text, then
// $0 and the positional arguments.
func (s *reviewedScript) command(arguments ...string) []string {
	return append([]string{"sh", "-c", s.text}, arguments...)
}

// provenanceProgram is the provenance analyzer that provenanceScript builds
// to "$bin" and then runs. It runs no go test.
const provenanceProgram = "tools/provenance/cmd/provenance"

var (
	bytecodeScript = &reviewedScript{
		name: "no tracked python bytecode",
		text: `tracked=$(git ls-files '*.pyc'); if [ -n "$tracked" ]; then echo "tracked Python bytecode (git rm it; __pycache__/ is ignored):"; echo "$tracked"; exit 1; fi`,
	}
	gofmtScript = &reviewedScript{
		name: "gofmt",
		text: `unformatted=$(gofmt -l "$@"); if [ -n "$unformatted" ]; then echo "gofmt needed:"; echo "$unformatted"; exit 1; fi`,
	}
	// stagedBuildScript builds $3 to the staging file $1 and renames it onto
	// $2 only when the bytes differ (stagedBuild).
	stagedBuildScript = &reviewedScript{
		name: "staged build",
		text: `mkdir -p "$(dirname "$1")" "$(dirname "$2")" && go build -buildvcs=false -o "$1" "$3" && { cmp -s "$1" "$2" || mv -f "$1" "$2"; }`,
		runs: []scriptRun{
			{argv: []string{"go", "build", "-buildvcs=false", "-o", "$1", "$3"}},
		},
	}
	// provenanceScript runs the provenance analyzer's controls uncached,
	// builds the analyzer to $1 (or a temporary directory) and runs it on the
	// checker with the manifest $2 (provenanceAnalyzer).
	provenanceScript = &reviewedScript{
		name: "provenance analyzer",
		text: `bin=${1:-$(mktemp -d)/provenance} && mkdir -p "$(dirname "$bin")" &&
go test -C tools/provenance -count=1 ` + testTimeout + ` ./... &&
go build -C tools/provenance -buildvcs=false -o "$bin" ./cmd/provenance &&
"$bin" -test=false -manifest "$2" ./internal/sketch/check`,
		runs: []scriptRun{
			{argv: []string{"go", "test", "-C", "tools/provenance", "-count=1", testTimeout, "./..."}},
			{argv: []string{"go", "build", "-C", "tools/provenance", "-buildvcs=false", "-o", "$bin", "./cmd/provenance"}},
			{argv: []string{"$bin", "-test=false", "-manifest", "$2", "./internal/sketch/check"}, program: provenanceProgram},
		},
	}
)

// reviewedScripts is the registry of the scripts a step may run.
var reviewedScripts = []*reviewedScript{bytecodeScript, gofmtScript, stagedBuildScript, provenanceScript}
