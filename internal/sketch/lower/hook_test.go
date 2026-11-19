package lower

import (
	"strings"
	"testing"
)

// TestHooksStayOutOfRunBuilds: a named point (std testing's Point and At,
// RT-HOOK) is lowered in the test build and leaves no call in the run
// build, where only an argument that is not a literal is still evaluated
// (mutants hook-run-build-keeps-points, hook-test-build-drops-points).
func TestHooksStayOutOfRunBuilds(t *testing.T) {
	const src = `import testing

func name() string { "p" }

func main() needs Console {
	testing.Point(name())
	testing.At("p", func() {})
	Console.Println("done")
}

test Hooks() {
	testing.At("p", func() {})
	testing.Point("p")
}
`
	run := mustLower(t, "hookrun", src).mainGo()
	for _, hook := range []string{"HookPoint", "HookAt"} {
		if strings.Contains(run, hook) {
			t.Errorf("the run build calls %s:\n%s", hook, run)
		}
	}
	if !strings.Contains(run, "name(") {
		t.Errorf("the run build dropped the point's argument name():\n%s", run)
	}
	test := lowerTestBuild(t, "hooktest", src).mainGo()
	for _, hook := range []string{"HookPoint", "HookAt"} {
		if !strings.Contains(test, hook) {
			t.Errorf("the test build does not call %s:\n%s", hook, test)
		}
	}
}
