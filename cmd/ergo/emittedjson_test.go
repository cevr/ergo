package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmittedJSONIsWhatTheCommandsGive: `ergo test --emitted --json DIR/...`
// gives, for each package, what `ergo check DIR/...` prints for it, the
// digest `ergo test --emitted` of its folder prints and, for the main
// package alone, the one `ergo build --emitted` prints; a package that does
// not check has no digest, and the call exits 1 as check does. The examples
// runner keys a program on this one call (gate-speed GS31b), so a digest
// that went stale here would replay a stale verdict there. It catches
// gs31b-test-digest-build-mode, gs31b-build-every-package and
// gs31b-digests-shared-load (cmd/ergo's ledger). The program requires
// another module, as examples/fieldnotes does: the shared load's main package
// then emits a go.mod without the runtime's require, unlike the build's.
func TestEmittedJSONIsWhatTheCommandsGive(t *testing.T) {
	t.Setenv("ERGO_CACHE", t.TempDir())
	t.Setenv("ERGO_LAWCACHE", "off")
	base := t.TempDir()
	smokeWrite(t, base, "dep/ergo.mod", "module ergo.dev/dep\n")
	smokeWrite(t, base, "dep/go.mod", "module ergo.dev/dep\n\ngo 1.27\n")
	smokeWrite(t, base, "dep/dep.ergo", "func Inc(x int) int { x + 1 }\n")
	root := filepath.Join(base, "app")
	smokeWrite(t, root, "ergo.mod", "module tree\n")
	smokeWrite(t, root, "go.mod", "module tree\n\ngo 1.27\n\nrequire ergo.dev/dep v0.0.0\n\nreplace ergo.dev/dep => ../dep\n")
	smokeWrite(t, root, "lib/lib.ergo", "import ergo.dev/dep\n\nfunc Twice(x int) int { dep.Inc(x) + x - 1 }\n\nlaw Doubles(x int) { Twice(x) == x + x }\n")
	smokeWrite(t, root, "cmd/app/main.ergo", "import tree/lib\n\ntype Shape = Dot{} | Box{Side int}\n\nfunc area(s Shape) int {\n\tmatch s {\n\tcase Dot:\n\t\t0\n\tcase Box as b:\n\t\tb.Side * b.Side\n\t}\n}\n\nfunc main() needs Console {\n\tConsole.Println(\"\\(lib.Twice(area(Box{Side: 3})))\")\n}\n")
	pattern := filepath.Join(root, "...")
	lines := func() ([]emittedLine, int) {
		t.Helper()
		out, code := commandInProcess(t, "test", "--emitted", "--json", pattern)
		var got []emittedLine
		for _, text := range bytes.Split(bytes.TrimRight(out, "\n"), []byte("\n")) {
			var l emittedLine
			if err := json.Unmarshal(text, &l); err != nil {
				t.Fatalf("not a line: %s: %v\n%s", text, err, out)
			}
			got = append(got, l)
		}
		return got, code
	}
	digest := func(args ...string) string {
		t.Helper()
		out, code := commandInProcess(t, args...)
		fields := strings.Fields(string(out))
		if code != 0 || len(fields) != 3 || fields[0] != "emitted" {
			t.Fatalf("ergo %s: exit %d\n%s", strings.Join(args, " "), code, out)
		}
		return fields[1]
	}
	got, code := lines()
	check, checkCode := commandInProcess(t, "check", pattern)
	if code != 0 || checkCode != 0 || len(got) != 2 {
		t.Fatalf("exit %d (check %d), %d lines: %+v", code, checkCode, len(got), got)
	}
	var envelopes bytes.Buffer
	for _, l := range got {
		envelopes.Write(l.Check)
		envelopes.WriteByte('\n')
	}
	if envelopes.String() != string(check) {
		t.Errorf("check envelopes:\n%s\nergo check printed:\n%s", envelopes.String(), check)
	}
	for _, l := range got {
		dir := filepath.Join(root, strings.TrimPrefix(strings.TrimPrefix(l.Package, "tree"), "/"))
		if want := digest("test", "--emitted", dir); l.Test != want {
			t.Errorf("%s: test digest %s; ergo test --emitted prints %s", l.Package, l.Test, want)
		}
		want := ""
		if l.Package == "tree/cmd/app" {
			want = digest("build", "--emitted", dir)
		}
		if l.Build != want || l.Error != "" {
			t.Errorf("%s: build digest %q (error %q); want %q", l.Package, l.Build, l.Error, want)
		}
	}
	smokeWrite(t, root, "lib/lib.ergo", "func Twice(x int) int { \"not an int\" }\n")
	got, code = lines()
	if code != 1 || len(got) == 0 || got[0].Package != "tree/lib" || got[0].Test != "" || got[0].Build != "" || !bytes.Contains(got[0].Check, []byte(`"checked":false`)) {
		t.Errorf("a package that does not check: exit %d, %+v", code, got)
	}
}
