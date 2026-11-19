// tn1record writes the feature record (TN1 input version 2,
// modules/tn1/record.ergo) of Ergo source files, as Ergo's parser fills
// it (internal/sketch/lint/features).
//
//	go run ./scripts/tn1record FILE            the record, to standard output
//	go run ./scripts/tn1record -out DIR FILE…  DIR/NAME.record for each FILE
//
// NAME is the file's base name without .ergo or .ergo.txt. A file that
// does not parse is an error: a record is only made from a syntax tree.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ergo.local/prototype/internal/sketch/lint/features"
	"ergo.local/prototype/internal/sketch/syntax"
)

func main() {
	out := flag.String("out", "", "write NAME.record files into this folder")
	flag.Parse()
	if flag.NArg() == 0 || *out == "" && flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: tn1record FILE | tn1record -out DIR FILE...")
		os.Exit(2)
	}
	for _, path := range flag.Args() {
		record, err := Record(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "tn1record:", err)
			os.Exit(1)
		}
		if *out == "" {
			fmt.Print(record)
			continue
		}
		name := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(path), ".txt"), ".ergo")
		if err := os.WriteFile(filepath.Join(*out, name+".record"), []byte(record), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "tn1record:", err)
			os.Exit(1)
		}
	}
}

// Record is the text form of the record of the Ergo file at path.
func Record(path string) (string, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	file, err := syntax.Parse(string(src))
	if err != nil {
		return "", fmt.Errorf("%s: %v", path, err)
	}
	return features.Encode(features.FromErgo(file)), nil
}
