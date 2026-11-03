// Command provenance runs the provenance analyzer over packages:
//
//	provenance -manifest internal/sketch/check/testdata/provenance-manifest.txt ./internal/sketch/check
package main

import (
	"ergo.local/prototype/tools/provenance"
	"golang.org/x/tools/go/analysis/singlechecker"
)

func main() { singlechecker.Main(provenance.Analyzer) }
