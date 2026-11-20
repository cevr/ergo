package check

import (
	"strings"
	"testing"
)

// TestStdIsOneModuleForUnfedLaws is the ruling that std is one module for
// a law no case can feed (LAW-UNFED; types.md, Laws 10): a std contract's
// provider that only another std package's named context builds is no
// error of its own package's check, its binding leaves that package's law
// runs, and the judgment over std's packages (ReportUnfed, which
// StdSnapshotNow runs) reports it only when no std context feeds it. It is
// the smallest repro of LLM's llm.Model laws (Retry fed by llm/memory's
// contexts). Mutant lu-std-no-module is its kill. A Go host type's law
// (atomic.Int64's) is judged by the same rule: fed by a context that
// builds the host value, and unfed with none (lu-host-type-excluded).
func TestStdIsOneModuleForUnfedLaws(t *testing.T) {
	const shape = "type Shape {\n\tArea() int,\n\tlaw Positive() { Area() > 0 },\n}\ntype Fn { F func(int) int } implements Shape\nfunc Area(s Fn) int { s.F(1) }\n" +
		"extern go ergo.local/prototype/runtime/v1.Int64 type Counter {\n\truntimeOnly(),\n\tLoad() int64,\n\tlaw Reads() { Load() == Load() },\n}\nextern go ergo.local/prototype/runtime/v1.NewInt64 func NewCounter(value int64) Counter\n"
	for _, row := range []struct {
		name, memory, want string
	}{
		{name: "fed by another std package", memory: "import zzshape\n\nlet overFn = context { zzshape.Shape: zzshape.Fn{F: func(n int) int { n + 1 }} }\n"},
		{name: "a Go host type fed by its context", memory: "import zzshape\n\nlet zero = context { zzshape.Counter: zzshape.NewCounter(0) }\n"},
		{name: "a Go host type fed by no context", memory: "import zzshape\n", want: "10:6: S108 law Counter.Reads cannot run: no package of module std provides Counter, so no case runs it; implement it in a package of the module, or bind a provider in a named context (types.md, Laws 10)"},
		{name: "fed by no std package", memory: "import zzshape\n\nfunc Size(s zzshape.Fn) int { s.F(1) }\n", want: "5:40: S108 law Shape.Positive, declared at shape.ergo:3:6, cannot run at Fn: its values have no generator: func(int) int is a function type; bind one in a named context of a test file, let Name = context { Shape: … }, and the law runs under it; no named context of a package of module std builds one (types.md, Laws 10)"},
	} {
		t.Run(row.name, func(t *testing.T) {
			lib := map[string]map[string]string{
				"zzshape":        {"shape.ergo": shape},
				"zzshape/memory": {"memory.ergo": row.memory},
			}
			s := newStdImporter()
			s.sources = func(path string) (map[string]string, bool) {
				files, ok := lib[path]
				return files, ok
			}
			var pkgs []*Package
			for _, path := range []string{"zzshape", "zzshape/memory"} {
				pkg, err := s.Import(path)
				if err != nil {
					t.Fatalf("import %s: %v; its check cannot see the std packages that import it", path, err)
				}
				pkgs = append(pkgs, pkg)
			}
			for _, r := range pkgs[0].LawRuns {
				for _, b := range r.Bindings {
					if len(b) > 0 && b[0].Type != nil && b[0].Type.Obj.Name == "Fn" {
						t.Fatalf("zzshape runs %s at Fn (unrun %q); the binding is std's to judge", r.Law.Title(pkgs[0]), b[0].Unrun)
					}
				}
			}
			var got []string
			for _, d := range ReportUnfed(pkgs)[pkgs[0]] {
				got = append(got, d.String())
			}
			if strings.Join(got, "\n") != row.want {
				t.Fatalf("std's judgment:\n%s\nwant\n%s", strings.Join(got, "\n"), row.want)
			}
		})
	}
}
