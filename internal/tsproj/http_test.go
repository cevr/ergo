package tsproj

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketchfront"
)

// TestHttpTableRefusesAmbiguousContracts pins projection diagnostics. It kills
// e3-missing, e3-repeat, e3-literal, e3-fields, e3-value, e3-binding, e3-bodies
// and e3-no-content. The existing RPC refusal test has no route table, and the
// stock-client example exercises only a valid table.
func TestHttpTableRefusesAmbiguousContracts(t *testing.T) {
	const read = `http.Route{Value: Read, Method: "GET", Path: "/r/:id", Status: 200}`
	const write = `http.Route{Value: Write, Method: "POST", Path: "/r", Status: 201}`
	const prelude = `import http
type Endpoint = Read{} | Write{}
type Reply { Value int }
type Bad { Reason string }
type API { Read(id int) Reply, Write(draft Reply) Reply fails Bad }
let Failures = []http.FailureStatus{http.FailureStatus{Tag: "Bad", Status: 422}}
`
	const table = `let Routes = []http.Route[Endpoint]{` + read + ", " + write + `}`
	for _, row := range []struct{ name, source, diagnostic string }{
		{"missing", prelude + strings.Replace(table, ", "+write, "", 1), "exactly once: missing"},
		{"repeated", prelude + strings.Replace(table, read, read+", "+strings.Replace(read, "/r/:id", "/other/:id", 1), 1), "exactly once: repeated Read"},
		{"computed table", prelude + `func routes() []http.Route[Endpoint] { []http.Route[Endpoint]{` + read + ", " + write + `} }\nlet Routes = routes()`, "constant literal slice"},
		{"computed field", prelude + `let method = "GET"` + "\n" + strings.Replace(table, `Method: "GET"`, `Method: method`, 1), "must be literals"},
		{"value alias", prelude + `let endpoint = Read` + "\n" + strings.Replace(table, "Value: Read", "Value: endpoint", 1), "Value must name an endpoint variant"},
		{"path binding", strings.Replace(prelude, "Read(id int)", "Read()", 1) + table, "every path parameter needs an operation parameter"},
		{"two payloads", strings.Replace(prelude, "Write(draft Reply)", "Write(draft Reply, other Reply)", 1) + table, "at most one JSON payload"},
		{"no-content result", prelude + strings.Replace(table, "Status: 201", "Status: 204", 1), "no-content response cannot carry a result"},
	} {
		t.Run(row.name, func(t *testing.T) {
			// This row describes a projection refusal, not a checker refusal.
			src := strings.ReplaceAll(row.source, `}\nlet`, "}\nlet")
			a := sketchfront.Check(src)
			if len(a.Diagnostics) != 0 {
				t.Fatal(a.Diagnostics)
			}
			_, err := Project(a.Root().Package, a.Root().Files[0].Syntax, src, Options{})
			if err == nil || !strings.Contains(err.Error(), row.diagnostic) {
				t.Fatalf("want %q, got %v", row.diagnostic, err)
			}
		})
	}
}

// TestHttpFailureTableDiagnostics kills e3b-missing, e3b-repeat,
// e3b-literal, e3b-status and e3b-unknown. Existing RPC diagnostics never
// bind failure types to HTTP statuses; the peer only uses a valid table.
func TestHttpFailureTableDiagnostics(t *testing.T) {
	const prelude = `import http
type Endpoint = Read{}
type Bad { Reason string }
type API { Read() fails Bad }
let Routes = []http.Route[Endpoint]{http.Route{Value: Read, Method: "POST", Path: "/", Status: 204}}
`
	const entry = `http.FailureStatus{Tag: "Bad", Status: 422}`
	const table = `let Failures = []http.FailureStatus{` + entry + `}`
	for _, row := range []struct{ name, source, want string }{
		{"missing", prelude, "missing status for failure Bad"},
		{"empty", prelude + strings.Replace(table, entry, "", 1), "missing status for failure Bad"},
		{"repeated", prelude + strings.Replace(table, entry, entry+", "+entry, 1), "repeated failure Bad"},
		{"computed table", prelude + `func failures() []http.FailureStatus { []http.FailureStatus{` + entry + `} }` + "\nlet Failures = failures()", "constant literal slice"},
		{"computed field", prelude + "let status = 422\n" + strings.Replace(table, "Status: 422", "Status: status", 1), "Tag and Status must be literals"},
		{"success status", prelude + strings.Replace(table, "422", "200", 1), "failure status (400..599)"},
		{"unknown", prelude + strings.Replace(table, entry, entry+`, http.FailureStatus{Tag: "Unknown", Status: 404}`, 1), "Unknown is not a contract failure"},
		{"two tables", prelude + table + "\n" + strings.Replace(table, "Failures", "Others", 1), "one exported failure table"},
	} {
		t.Run(row.name, func(t *testing.T) {
			a := sketchfront.Check(row.source)
			if len(a.Diagnostics) != 0 {
				t.Fatal(a.Diagnostics)
			}
			_, err := Project(a.Root().Package, a.Root().Files[0].Syntax, row.source, Options{})
			if err == nil || !strings.Contains(err.Error(), row.want) {
				t.Fatalf("want %q, got %v", row.want, err)
			}
		})
	}
}
