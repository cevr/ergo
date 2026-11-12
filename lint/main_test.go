package lint

import (
	"os"
	"testing"

	"ergo.local/prototype/internal/testinputs"
)

// TestMain runs the tests under observation, so the files and variables the
// fixture pack's build and process read reach Go's test cache, and removes
// the built fixture pack.
func TestMain(m *testing.M) {
	code := testinputs.Main(m)
	if fixture.dir != "" {
		os.RemoveAll(fixture.dir)
	}
	os.Exit(code)
}
