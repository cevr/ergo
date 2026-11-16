package policy

import (
	"os"
	"testing"

	"ergo.local/prototype/internal/testinputs"
)

// TestMain runs the tests under observation, so the fixtures, the
// configurations and the manifest they read reach Go's test cache.
func TestMain(m *testing.M) { os.Exit(testinputs.Main(m)) }
