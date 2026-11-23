package lower_test

import (
	"os"

	"ergo.local/prototype/internal/experimental/irspike"
	"ergo.local/prototype/internal/sketch/lower"
)

// The IR leg of TestGoCallsErgo (LIB-ON-IR) prints a library's IR bodies
// with irspike, which imports lower, so the hook is set from this external
// test package. The toolchains are built once, on the first use.
func init() {
	lower.LibraryTextForTest = func(files []lower.File, name string) ([]byte, error) {
		_, tc, err := irspike.ErgoToolchains()
		if err != nil {
			return nil, err
		}
		return irspike.LibraryText(files, tc, name, os.Stderr)
	}
}
