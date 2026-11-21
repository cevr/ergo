// Command run writes ot/opentui.go from ot/opentui.h and
// ot/opentui.bind.json (go generate in ot/ runs it there).
package main

import (
	"fmt"
	"os"

	"ergo.local/prototype/hosts/opentui/ot/internal/otgen"
)

func main() {
	src, err := otgen.Source(".")
	if err == nil {
		err = os.WriteFile("opentui.go", src, 0o644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "otgen:", err)
		os.Exit(1)
	}
}
