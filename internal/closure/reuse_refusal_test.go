package closure

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/testinputs"
)

// TestReuseRefusesWhatASweepWouldNotSee: the sweep reuses recorded runs
// only when each carries every guarantee of a swept run and together they
// ran every test, and a refusal names what is missing. Each case breaks one
// guarantee of a run that is otherwise reused. In process: the records are
// files. Mutants: heavy-cost2-reuse-env-unchecked,
// heavy-cost2-reuse-coverage-unchecked, heavy-cost2-reuse-userns-unchecked,
// heavy-cost2-reuse-cache-unchecked, heavy-cost2-reuse-short-run.
func TestReuseRefusesWhatASweepWouldNotSee(t *testing.T) {
	const pkg = "internal/p"
	pkgDir := "/nonexistent/loop-probe-x/m/internal/p"
	env := []string{"PATH=/bin", "HOME=/nonexistent/loop-probe-x/home"}
	names := []string{"TestA", "TestB"}
	good := func(base string) testinputs.Record {
		return testinputs.Record{
			Package: pkg, Dir: pkgDir, Args: []string{"-test.v=true"},
			Env:   append([]string{"TMPDIR=/nonexistent/loop-probe-x/t", testinputs.RecordVariable + "=" + base}, env...),
			Cache: filepath.Join(base, "cache", "internal_p"), TreeReadOnly: true, UserNamespaces: testinputs.UserNamespacesRefused + "fork/exec /bin/true: no space left on device",
		}
	}
	for _, c := range []struct {
		name   string
		change func(*testinputs.Record)
		log    string
		want   string
	}{
		{"reused", func(*testinputs.Record) {}, "", ""},
		{"writable tree", func(r *testinputs.Record) { r.TreeReadOnly, r.TreeProbe = false, "the module root is writable" }, "", "checkout writable"},
		{"user namespaces", func(r *testinputs.Record) { r.UserNamespaces = "allowed" }, "", "user namespaces"},
		{"shared cache", func(r *testinputs.Record) { r.Cache = "/nonexistent/loop-probe-x/gocache" }, "", "cache of its package's own"},
		{"tier variable", func(r *testinputs.Record) { r.Env = append(r.Env, "ERGO_TEST_CLI=bin/ergo") }, "", "ERGO_TEST_CLI is set in the recorded run only"},
		{"other value", func(r *testinputs.Record) { r.Env[2] = "PATH=/usr/bin" }, "", `PATH="/usr/bin" in the recorded run`},
		{"short run", func(r *testinputs.Record) { r.Args = append(r.Args, "-test.short=true") }, "", "-short"},
		{"part of the tests", func(r *testinputs.Record) { r.Args = append(r.Args, "-test.run", "^TestA$") }, "", "ran 1 of the package's tests, TestB first"},
		{"subtests", func(r *testinputs.Record) { r.Args = append(r.Args, "-test.run=^TestA$/x") }, "", "subtests"},
		{"failed", func(r *testinputs.Record) { r.Exit = 1 }, "", "exited 1"},
		{"unobserved", func(*testinputs.Record) {}, "# test log\ntestinputs: unobserved: a reason", "not observed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			base := t.TempDir()
			dir := filepath.Join(base, "runs", "internal_p", "1-a")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			log := c.log
			if log == "" {
				log = "# test log\nopen x\n"
			}
			for name, data := range map[string]string{testinputs.RecordLog: log, testinputs.RecordIdentity: "{}"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			r := good(base)
			c.change(&r)
			why := reuseRefusal([]testinputs.Record{r}, []string{dir}, pkg, pkgDir, names, env)
			if c.want == "" && why != "" || c.want != "" && !strings.Contains(why, c.want) {
				t.Fatalf("refusal %q; want %q", why, c.want)
			}
		})
	}
}
