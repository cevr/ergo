// Command exportgen writes the generated export codecs from the source of
// package check and package syntax:
//
//	go run ./scripts/exportgen [-check]
//
// It writes internal/sketch/check/snapshot/codec_gen.go,
// internal/sketch/check/codec_access_gen.go and
// internal/sketch/syntax/codec_gen.go. With -check it writes nothing and
// exits 1 when a file differs from what it would write.
//
// It is the regeneration entry that builds nothing it generates for: it
// imports internal/sketch/exportgen only, which type-checks check from its
// source without the generated accessors (exportgen.Generate). So it runs
// after a merge that changed check's types and left codec_access_gen.go
// stale, when package check, and so the freshness test
// TestExportCodecsAreCurrent in check/snapshot, no longer compiles.
// TestEntryBuildsNoGeneratedPackage (internal/sketch/exportgen) holds that
// it imports neither.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"ergo.local/prototype/internal/sketch/exportgen"
)

func main() {
	check := flag.Bool("check", false, "write nothing; exit 1 when a generated file is stale")
	root := flag.String("root", ".", "the module's root")
	flag.Parse()
	srcs, err := exportgen.Generate(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "exportgen:", err)
		os.Exit(2)
	}
	paths := make([]string, 0, len(srcs))
	for rel := range srcs {
		paths = append(paths, rel)
	}
	sort.Strings(paths)
	stale := false
	for _, rel := range paths {
		path := filepath.Join(*root, rel)
		got, _ := os.ReadFile(path)
		same := bytes.Equal(got, srcs[rel])
		if *check {
			if !same {
				fmt.Printf("stale %s\n", rel)
				stale = true
			}
			continue
		}
		// Every file is written, the same bytes too, so that `ergo task
		// gen` sees it as this generator's output (docs/spec/build.md,
		// Generators: outputs are observed by their stat).
		if err := os.WriteFile(path, srcs[rel], 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "exportgen:", err)
			os.Exit(2)
		}
		if !same {
			fmt.Printf("wrote %s\n", rel)
		}
	}
	if stale {
		os.Exit(1)
	}
}
