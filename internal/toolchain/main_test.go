package toolchain

import (
	"os"
	"testing"

	"ergo.local/prototype/internal/testinputs"
)

// TestMain observes what the tests read (testinputs), so Go's test cache
// and the closure sweep see every input.
func TestMain(m *testing.M) { os.Exit(testinputs.Main(m)) }
