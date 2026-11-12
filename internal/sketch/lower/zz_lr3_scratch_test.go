package lower

import (
	"os"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// Scratch (LR3, never committed): dump std json's test build as root.
func TestZZLR3DumpStdJSON(t *testing.T) {
	out := os.Getenv("LR3_DUMP")
	if out == "" {
		t.Skip()
	}
	pkg, err := check.Std.Import("json")
	if err != nil {
		t.Fatal(err)
	}
	var sources []Source
	for _, f := range pkg.Files() {
		sources = append(sources, Source{Path: "/ergo-std/json/" + f.Name, Text: f.Src})
	}
	u := Unit{Package: pkg, Files: pkg.Files(), Sources: sources}
	files, err := LowerTests(u)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if err := os.WriteFile(out+"/"+sanitize(f.Path), f.Data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func sanitize(p string) string {
	b := []byte(p)
	for i, c := range b {
		if c == '/' {
			b[i] = '_'
		}
	}
	return string(b)
}
