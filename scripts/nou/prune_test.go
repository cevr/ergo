package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestPruneWriteReportsExactRemovals(t *testing.T) {
	name := filepath.Join(t.TempDir(), "sites.tsv")
	const before = "D\tcompiler\tcompiler/f.ergo\tfunc gone/a\t1\nD\tcompiler\tcompiler/f.ergo\tfunc keep/b\t3\n"
	if err := os.WriteFile(name, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	rows, err := readLedger(bytes.NewBufferString(before))
	if err != nil {
		t.Fatal(err)
	}
	have := map[siteID]int{{"compiler/f.ergo", "func keep/b"}: 2}
	read := func() string {
		t.Helper()
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	var dry, output bytes.Buffer
	if err := pruneLedger(name, false, &dry, rows, have); err != nil {
		t.Fatal(err)
	}
	if read() != before {
		t.Fatal("dry prune changed the file")
	}
	if err := pruneLedger(name, true, &output, rows, have); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != dry.String() || !bytes.Contains(dry.Bytes(), []byte("func keep/b\t2\n")) ||
		bytes.Contains(dry.Bytes(), []byte("func gone/a")) {
		t.Fatalf("written prune must match the dry projection and keep exactly two surviving occurrences: %q", got)
	}
	const want = "removed\tD\tcompiler\tcompiler/f.ergo\tfunc gone/a\t1\n" +
		"removed\tD\tcompiler\tcompiler/f.ergo\tfunc keep/b\t1\n2 legacy sites removed\n"
	if got := output.String(); got != want {
		t.Fatalf("removal report:\ngot  %q\nwant %q", got, want)
	}
}
