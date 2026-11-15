package sketchfront

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/gobind"
)

// TestStdBindingRefusedOffItsPlatforms is the text and position of the
// S104 an import of Go's standard library gets on a build platform its
// binding does not state (stdUnbound): windows/amd64 refuses `import go
// strings`, which linux/amd64 admits. Mutant: stdUnbound always "" (the
// import would check and link a binding nobody read for Windows).
func TestStdBindingRefusedOffItsPlatforms(t *testing.T) {
	const src = "import go strings\n\nfunc main() {\n\tlet _ = strings.ToUpper(\"a\")\n}\n"
	if d := CheckForPlatform(src, gobind.Platform{GOOS: "linux", GOARCH: "amd64"}).Diagnostics; len(d) != 0 {
		t.Fatalf("linux/amd64: %v", d)
	}
	d := CheckForPlatform(src, gobind.Platform{GOOS: "windows", GOARCH: "amd64"}).Diagnostics
	if len(d) == 0 {
		t.Fatal("windows/amd64: no diagnostic")
	}
	want := `import go "strings": Go's standard-library binding states platforms darwin/amd64 darwin/arm64 linux/amd64 linux/arm64; target is windows/amd64 (docs/spec/interop.md, Platforms)`
	if d[0].Code != "S104" || d[0].Message != want || d[0].Span.Line != 1 || d[0].Span.Column != 11 {
		t.Fatalf("windows/amd64: %s %d:%d %s\nwant S104 1:11 %s", d[0].Code, d[0].Span.Line, d[0].Span.Column, d[0].Message, want)
	}
	if strings.Contains(d[0].Message, "ergo bind go") {
		t.Fatalf("the refusal offers a command that cannot help: %s", d[0].Message)
	}
}
