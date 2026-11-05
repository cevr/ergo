package v1

import (
	"os"
	"strconv"
)

// The bodies of std os (docs/spec/stdlib.md, host packages). Each is a
// top-level function of basic and slice types with no hidden argument, so
// BodyOf binds it as written. The names carry their std package, because
// the lowering keys runtime bindings by bare name. They read the host
// process, so none is pure.

// Exit is os.Exit, the failure that ends main with status Code. When it is
// main's only reason, report writes Message verbatim (docs/spec/runtime.md,
// Exit causes). It is a record of a v1 package, so the lowering binds
// os.Exit to this Go type (docs/spec/stdlib.md, Types 4).
type Exit struct {
	Code    int
	Message string
}

// Error is the failure's text: its Ergo name and fields, as the lowering
// writes a record's. report writes it when Exit is not main's only reason.
func (e Exit) Error() string {
	return "os.Exit{Code:" + strconv.Itoa(e.Code) + " Message:" + e.Message + "}"
}

// OSArgs is the body of os.Host's Args: the arguments after the program
// name, in a fresh slice. A process started with no argv at all has none.
func OSArgs() []string {
	if len(os.Args) < 2 {
		return []string{}
	}
	return append([]string{}, os.Args[1:]...)
}

// OSHasEnv reports whether the environment variable key is set, even to
// the empty string.
func OSHasEnv(key string) bool {
	_, ok := os.LookupEnv(key)
	return ok
}

// OSGetenv is the value of the environment variable key; it is empty when
// key is unset, so os.Host's Getenv asks OSHasEnv first.
func OSGetenv(key string) string { return os.Getenv(key) }
