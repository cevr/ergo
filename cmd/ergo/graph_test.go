package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
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

// TestGraphColumnsAreTheJSON runs every query of `ergo graph` over the
// rows of examples/pipeline twice, as the binary hands them over (columns,
// RunColumns) and as a rows file is read (JSON, Run), with the same rows
// as the base for affected: the two outputs are equal. Mutant
// graph-columns-drop-how (internal/sketch/lower/testdata/mutants.json): a
// use's how missing from the columns changes hidden's output.
func TestGraphColumnsAreTheJSON(t *testing.T) {
	rows, err := graphRows("../../examples/pipeline/...")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	strs, ints := rows.AppendColumns(nil, nil)
	base, bints := rows.AppendColumns(strs, ints)
	for _, args := range [][]string{
		{"cycles", "--json"},
		{"cycles", "--level", "file", "--json"},
		{"hidden", "--json"},
		{"chains", "--json"},
		{"fan", "--json"},
		{"files", "--json"},
		{"directories", "--json"},
		{"field-writes", "--json"},
		{"test-support", "--json"},
		{"seams", "--json"},
		{"folder-cycles", "--json"},
		{"layers", "--json"},
		{"why", "pipeline/run", "slices", "--json"},
		{"hidden"},
	} {
		viaJSON, err := lgraph.Run(context.Background(), args, string(data), "")
		if err != nil {
			t.Fatal(err)
		}
		viaColumns, err := lgraph.RunColumns(context.Background(), args, strs, ints)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(viaJSON, viaColumns) {
			t.Errorf("ergo graph %v: columns %q, JSON %q", args, viaColumns, viaJSON)
		}
	}
	args := []string{"affected", "base", "--json"}
	viaJSON, err := lgraph.Run(context.Background(), args, string(data), string(data))
	if err != nil {
		t.Fatal(err)
	}
	viaColumns, err := lgraph.RunColumns(context.Background(), args, base, bints)
	if err != nil || !slices.Equal(viaJSON, viaColumns) {
		t.Errorf("ergo graph affected: columns %q %v, JSON %q", viaColumns, err, viaJSON)
	}
}
