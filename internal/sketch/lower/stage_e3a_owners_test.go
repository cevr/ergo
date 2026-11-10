package lower

import ()

// Stage E3a round 2 M2 (check/owners.go, lower/owners.go): a test owns the
// emission facts its body introduces, so only the test build reads them.
// The property: for every production base × every test that alone
// introduces one emission fact, the run build is the run build of the same
// source with the test (and an import only the test uses) blanked out, the
// run binary prints the base's output, and the test build passes. A type
// that only a test's error row makes an error type gets an Error method and
// a text helper in the test build and neither in the run build.

// ownerBase is a production program: declarations and a main that prints
// out. The bases cover an ordinary program, a generic one, one with a
// context and one with a failure of its own.
type ownerBase struct{ name, decls, main, out string }

var ownerBases = []ownerBase{
	{"ordinary", "", "func main() needs Console {\n\tConsole.Println(\"run\")\n}\n", "run"},
	{"generic",
		"type Cell[T] { V T }\n\nfunc first[T](xs []T, fallback T) T {\n\tfor x in xs {\n\t\treturn x\n\t}\n\tfallback\n}\n",
		"func main() needs Console {\n\tConsole.Println(first([]string{\"g\"}, Cell{V: \"z\"}.V))\n}\n", "g"},
	{"context",
		"type Watch {\n\tNow() string,\n}\n\ntype Sys {}\n\nfunc Now(_ Sys) string {\n\t\"now\"\n}\n\nlet Run = context {\n\tWatch: Sys,\n}\n",
		"func main() needs Console {\n\tConsole.Println(with Run { Watch.Now() })\n}\n", "now"},
	{"failure",
		"type Oops { Code int }\n\nfunc risky(n int) int fails Oops {\n\tif n < 0 {\n\t\treturn Oops{Code: n}\n\t}\n\tn\n}\n",
		"func main() needs Console {\n\tlet v = risky(-4) catch {\n\tcase Oops as e:\n\t\te.Code\n\t}\n\tConsole.Println(\"\\(v)\")\n}\n", "-4"},
}
