package tsproj

import (
	"ergo.local/prototype/internal/testinputs"
	"os"
	"testing"
)

// TestMain records the sources and environment used by the opt-in TS leg.
func TestMain(m *testing.M) { os.Exit(testinputs.Main(m)) }
