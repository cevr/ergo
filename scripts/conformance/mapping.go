package main

// Validate the selected Effect behavior mapping. Each case records where its
// behavior is specified upstream (file, line and test label at the pinned
// commit) as provenance text; Ergo keeps no copy of the upstream corpus, so
// the record is checked for shape, and its evidence must be an existing Go
// test in a *_test.go file that Git tracks in the repository. The test
// proves its case in a subtest named after the case id: the test's body
// holds t.Run("<id>", ...), and check --run requires that subtest to run
// and pass.

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

const mappingRelative = "conformance/effect-cases.json"

var (
	mappingStatuses = []string{"covered", "difference", "pending", "unsupported"}
	mappingCaseID   = regexp.MustCompile(`^effect\.[a-z0-9.-]+$`)
	mappingTestName = regexp.MustCompile(`^Test[A-Za-z0-9_]+$`)
)

// upstreamTestComponents are the path components that mark an upstream file
// as a test, as the pinned selection rule chose them.
var upstreamTestComponents = []string{"test", "tests", "typetest", "test-dts"}

func readMapping(path string) (map[string]any, error) {
	text, err := readText(path)
	var value any
	if err == nil {
		value, err = decodeJSON(text)
	}
	if err != nil {
		return nil, errors.New("mapping is not valid UTF-8 JSON")
	}
	mapping, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("mapping root must be an object")
	}
	return mapping, nil
}

// hasExactKeys is Python's set(value) == {keys...} for a json.loads object.
func hasExactKeys(value any, keys ...string) bool {
	object, ok := value.(map[string]any)
	if !ok || len(object) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, ok := object[key]; !ok {
			return false
		}
	}
	return true
}

// positiveLine returns a json.loads int (never a bool or float) when it is at
// least 1, saturating lines too large for any file.
func positiveLine(value any) (int, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	line, ok := jsonInt(number)
	if !ok || line.Sign() < 1 {
		return 0, false
	}
	if !line.IsInt64() || line.Int64() > int64(^uint(0)>>1) {
		return int(^uint(0) >> 1), true
	}
	return int(line.Int64()), true
}

// evidenceTest is one evidence test: its Go package, as a ./-relative
// directory of the repository, and its name.
type evidenceTest struct {
	pkg, name string
}

// evidenceClaim is one case an evidence test claims to prove: the test must
// run the subtest named caseID, and it must pass.
type evidenceClaim struct {
	test   evidenceTest
	caseID string
}

// subtest is the name go test gives the claim's subtest.
func (c evidenceClaim) subtest() string { return c.test.name + "/" + c.caseID }

// runsSubtest reports whether the top-level function test of the Go file
// contents calls <x>.Run with the string literal id as its first argument
// anywhere in its body. Comments, other strings and other functions do not
// count.
func runsSubtest(contents, test, id string) bool {
	file, err := parser.ParseFile(token.NewFileSet(), "", contents, parser.SkipObjectResolution)
	if err != nil {
		return false
	}
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv != nil || function.Name.Name != test || function.Body == nil {
			continue
		}
		found := false
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || found || len(call.Args) == 0 {
				return !found
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			literal, isLiteral := call.Args[0].(*ast.BasicLit)
			if ok && selector.Sel.Name == "Run" && isLiteral && literal.Kind == token.STRING {
				value, err := strconv.Unquote(literal.Value)
				found = err == nil && value == id
			}
			return !found
		})
		return found
	}
	return false
}

// validateMapping validates the mapping and its evidence tests under root and
// returns the evidence tests, sorted by package and name, each once, and the
// case claims, sorted by test and case.
func validateMapping(mapping map[string]any, root string) ([]evidenceTest, []evidenceClaim, error) {
	if !hasExactKeys(mapping, "schemaVersion", "sourceCommit", "cases") || pyType(mapping["schemaVersion"]) != "int" || !isPyInt(mapping["schemaVersion"], 1) || mapping["sourceCommit"] != pinnedCommit {
		return nil, nil, errors.New("mapping schema or upstream pin is invalid")
	}
	cases, ok := mapping["cases"].([]any)
	if !ok || len(cases) == 0 {
		return nil, nil, errors.New("mapping cases must be a nonempty list")
	}
	ids := map[string]bool{}
	type upstreamCase struct {
		path string
		line int
	}
	upstreamCases := map[upstreamCase]bool{}
	tests := map[evidenceTest]bool{}
	claims := map[evidenceClaim]bool{}
	for _, value := range cases {
		if !hasExactKeys(value, "id", "status", "upstream", "behavior", "limits", "evidence") {
			return nil, nil, errors.New("mapping case fields are invalid")
		}
		item := value.(map[string]any)
		identity, ok := item["id"].(string)
		if !ok || !mappingCaseID.MatchString(identity) || ids[identity] {
			return nil, nil, errors.New("mapping case id is invalid or duplicated")
		}
		ids[identity] = true
		status, ok := item["status"].(string)
		if !ok || !slices.Contains(mappingStatuses, status) {
			return nil, nil, fmt.Errorf("invalid status for %s", identity)
		}
		for _, key := range []string{"behavior", "limits"} {
			if text, ok := item[key].(string); !ok || pyStrip(text) == "" {
				return nil, nil, fmt.Errorf("behavior and limits are required for %s", identity)
			}
		}
		if !hasExactKeys(item["upstream"], "file", "line", "label") {
			return nil, nil, fmt.Errorf("invalid upstream case for %s", identity)
		}
		upstream := item["upstream"].(map[string]any)
		path, err := safeRelativePath(upstream["file"])
		if err != nil {
			return nil, nil, err
		}
		line, lineOK := positiveLine(upstream["line"])
		label, labelOK := upstream["label"].(string)
		if !isUpstreamTest(path) || !lineOK || !labelOK || pyStrip(label) == "" {
			return nil, nil, fmt.Errorf("upstream target is invalid for %s", identity)
		}
		key := upstreamCase{path, line}
		if upstreamCases[key] {
			return nil, nil, fmt.Errorf("duplicate upstream case for %s", identity)
		}
		upstreamCases[key] = true
		evidence, ok := item["evidence"].([]any)
		if !ok || (len(evidence) > 0) != (status == "covered" || status == "difference") {
			return nil, nil, fmt.Errorf("status/evidence misrepresentation for %s", identity)
		}
		for _, value := range evidence {
			if !hasExactKeys(value, "file", "test", "targets") {
				return nil, nil, fmt.Errorf("invalid evidence for %s", identity)
			}
			pointer := value.(map[string]any)
			file, err := safeRelativePath(pointer["file"])
			if err != nil {
				return nil, nil, err
			}
			test, testOK := pointer["test"].(string)
			if !strings.HasSuffix(file, "_test.go") || !testOK || !mappingTestName.MatchString(test) || !isGoTargets(pointer["targets"]) {
				return nil, nil, fmt.Errorf("evidence must name an existing Go test for %s", identity)
			}
			if err := requireTracked(root, file); err != nil {
				return nil, nil, fmt.Errorf("evidence file is not tracked for %s: %v", identity, err)
			}
			contents, err := readText(filepath.Join(root, file))
			if err != nil {
				return nil, nil, fmt.Errorf("evidence file is unavailable for %s", identity)
			}
			declaration := regexp.MustCompile(`(?m)^func ` + regexp.QuoteMeta(test) + `\(t \*testing\.T\) \{`)
			if !declaration.MatchString(contents) {
				return nil, nil, fmt.Errorf("evidence test is unavailable for %s", identity)
			}
			if !runsSubtest(contents, test, identity) {
				return nil, nil, fmt.Errorf("evidence test %s runs no subtest %q for %s", test, identity, identity)
			}
			named := evidenceTest{pkg: packageOf(file), name: test}
			tests[named] = true
			claims[evidenceClaim{test: named, caseID: identity}] = true
		}
	}
	selected := make([]evidenceTest, 0, len(tests))
	for test := range tests {
		selected = append(selected, test)
	}
	sort.Slice(selected, func(i, j int) bool {
		if selected[i].pkg != selected[j].pkg {
			return selected[i].pkg < selected[j].pkg
		}
		return selected[i].name < selected[j].name
	})
	claimed := make([]evidenceClaim, 0, len(claims))
	for claim := range claims {
		claimed = append(claimed, claim)
	}
	sort.Slice(claimed, func(i, j int) bool {
		a, b := claimed[i], claimed[j]
		if a.test != b.test {
			return a.test.pkg < b.test.pkg || a.test.pkg == b.test.pkg && a.test.name < b.test.name
		}
		return a.caseID < b.caseID
	})
	return selected, claimed, nil
}

// requireTracked refuses a repository-relative file that Git does not track
// in root, so an untracked local test cannot stand as evidence.
func requireTracked(root, file string) error {
	command := exec.Command("git", "--no-optional-locks", "-C", root, "ls-files", "--error-unmatch", "--", file)
	if output, err := command.CombinedOutput(); err != nil {
		if message := strings.TrimSpace(string(output)); message != "" {
			return errors.New(message)
		}
		return err
	}
	return nil
}

// packageOf is the go test package pattern of a repository-relative file.
func packageOf(file string) string {
	directory := filepath.ToSlash(filepath.Dir(file))
	if directory == "." {
		return "."
	}
	return "./" + directory
}

// testEvent is the part of a go test -json event the run check reads.
type testEvent struct {
	Action, Test, Output string
}

// checkClaimsRan reads the go test -json events of one package's evidence
// run, writes their output text to out, and fails unless every claim of
// that package (pkg) ran its subtest and the subtest passed. A subtest that
// was skipped, failed or never started fails its claim.
func checkClaimsRan(events io.Reader, pkg string, claims []evidenceClaim, out io.Writer) error {
	passed := map[string]bool{}
	decoder := json.NewDecoder(events)
	for {
		var event testEvent
		if err := decoder.Decode(&event); err == io.EOF {
			break
		} else if err != nil {
			return fmt.Errorf("go test -json output of %s is not JSON: %v", pkg, err)
		}
		if event.Action == "output" {
			io.WriteString(out, event.Output)
		}
		if event.Test != "" && event.Action == "pass" {
			passed[event.Test] = true
		}
	}
	var missing []string
	for _, claim := range claims {
		if claim.test.pkg == pkg && !passed[claim.subtest()] {
			missing = append(missing, claim.subtest())
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("evidence subtests of %s did not run and pass: %s", pkg, strings.Join(missing, ", "))
	}
	return nil
}

// evidenceCommands is one go test -json command line per package of the
// evidence tests, in package order, each selecting only that package's
// tests.
func evidenceCommands(tests []evidenceTest) [][]string {
	var commands [][]string
	for start := 0; start < len(tests); {
		end := start
		var names []string
		for end < len(tests) && tests[end].pkg == tests[start].pkg {
			names = append(names, tests[end].name)
			end++
		}
		commands = append(commands, []string{"go", "test", "-json", tests[start].pkg, "-run", "^(" + strings.Join(names, "|") + ")$", "-count=1"})
		start = end
	}
	return commands
}

// isUpstreamTest reports whether an upstream path names a test file: a path
// with a test component below it.
func isUpstreamTest(path string) bool {
	parts := pathParts(path)
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts[:len(parts)-1] {
		if slices.Contains(upstreamTestComponents, part) {
			return true
		}
	}
	return false
}

// isGoTargets reports whether value is exactly ["go"].
func isGoTargets(value any) bool {
	targets, ok := value.([]any)
	return ok && len(targets) == 1 && targets[0] == "go"
}
