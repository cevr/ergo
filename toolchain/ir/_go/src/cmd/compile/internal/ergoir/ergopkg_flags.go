package ergoir

import "flag"

// PkgFlag enables the checked-package EIR v1 ingress (-ergopkg).
// Registered on flag.CommandLine here so base/flag.go needs no hook hunk.
var PkgFlag bool

func init() {
	flag.CommandLine.BoolVar(&PkgFlag, "ergopkg", false, "compile a checked EIR v1 package without Go parsing or types2")
}
