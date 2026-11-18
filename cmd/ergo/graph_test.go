package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	lgraph "ergo.local/prototype/internal/linked/graph"
)

// TestGraphQueriesReadTheExport runs `ergo graph hidden --json` in
// process (graphRows, then the linked Ergo over the rows' JSON) on a
// module whose main reads a field of a type of a package it does not
// import: the export's JSON decodes as std codegraph's Rows, and the
// read is the one hidden use. Mutant graph-json-drops-file
// (internal/sketch/lower/testdata/mutants.json): a row field the export
// leaves out fails std json's decode.
func TestGraphQueriesReadTheExport(t *testing.T) {
	dir := t.TempDir()
	for name, text := range map[string]string{
		"ergo.mod":        "module m\n",
		"b/b.ergo":        "type T { X int }\n\nfunc New() T { T{X: 1} }\n",
		"a/a.ergo":        "import m/b\n\nfunc Make() b.T { b.New() }\n",
		"cmd/m/main.ergo": "import m/a\n\nfunc main() needs Console {\n\tConsole.Println(\"\\(a.Make().X)\")\n}\n",
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := graphRows(dir + "/...")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	out, err := lgraph.Run(context.Background(), []string{"hidden", "--json"}, string(data), "")
	if err != nil || len(out) != 3 || out[0] != "0" {
		t.Fatalf("ergo graph hidden: %v %q", err, out)
	}
	var hidden []struct{ From, To, Package, Via, How string }
	if err := json.Unmarshal([]byte(out[1]), &hidden); err != nil {
		t.Fatalf("%v: %s", err, out[1])
	}
	want := struct{ From, To, Package, Via, How string }{"m/cmd/m.main", "m/b.T.X", "m/b", "", "read"}
	if len(hidden) != 1 || hidden[0] != want {
		t.Fatalf("hidden uses %+v, want only %+v", hidden, want)
	}
}
