package main

// A gate step may run a shell script (sh -c SCRIPT) only when the script is
// one of reviewedScripts, byte for byte. No program can read what a shell
// script runs: a parameter expansion, a glob or eval can name any program in
// a form no reader sees (gate-testcache-gap rounds 1 and 2). The gate does
// not need to: every step runs with the uncached default in GOFLAGS
// (stepEnvironment), and every process a script starts inherits it. The
// registry keeps each script in one reviewed place, where the policy test
// (TestTestCommandsHaveCacheCoverage) refuses a script that names GOFLAGS or
// gives -count a value other than 1, and TestReviewedScripts refuses an entry
// that no step runs.
//
// The steps take their scripts from these values, so a step and its entry
// cannot differ. To add or change a script, change its entry here and have
// it reviewed.
type reviewedScript struct {
	// name says what the script does.
	name string
	// text is the script, as the step gives it to sh -c.
	text string
}

// command returns the step argv that runs the script: sh -c, the text, then
// $0 and the positional arguments.
func (s *reviewedScript) command(arguments ...string) []string {
	return append([]string{"sh", "-c", s.text}, arguments...)
}

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
	}
)

// reviewedScripts is the registry of the scripts a step may run.
var reviewedScripts = []*reviewedScript{bytecodeScript, gofmtScript, stagedBuildScript, provenanceScript}
