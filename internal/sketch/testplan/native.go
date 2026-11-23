package testplan

// NativeRoot is a root in a test run plan. A required native-only root
// accounts for missing law verdicts in this run and cannot recover from
// an invocation failure by trying a different package or runner.
type NativeRoot struct {
	Path     string
	Required bool
}
