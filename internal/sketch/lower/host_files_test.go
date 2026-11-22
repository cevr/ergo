package lower

import (
	"regexp"
	"strings"
	"testing"
)

// hostReadLoop copies one file to another through an open handle with only
// Ambient (HOST-ROW-3 slice 2): os.Open, os.Create and the *os.File
// methods are host rows, and File.Read writes its buffer in place
// (v1.GoWrites), so its value is (the buffer Go wrote, n).
const hostReadLoop = `import go os

func copyFile(from string, to string, size int) int fails go.Error needs Ambient {
	let src = try os.Open(from)
	let dst = try os.Create(to)
	let mut buf = []byte{0, 0, 0, 0, 0, 0, 0, 0}
	let mut total = 0
	for total < size {
		let (b, n) = try src.Read(buf)
		let _ = try dst.Write(b[:n])
		buf = b
		total = total + n
	}
	let _ = try src.Seek(0, 0)
	let (whole, m) = try src.ReadAt(buf, 0)
	try dst.Sync()
	try dst.Close()
	try src.Close()
	total + m + len(whole)
}

func main() needs Console | Ambient {
	let n = copyFile("/nonexistent/loop-probe-x", "/nonexistent/loop-probe-y", 16) catch { case go.Error: -1 }
	Console.Println("\(n)")
}
`

// TestFileHandlesNeedOnlyAmbient: a read loop over an open file checks
// with Ambient alone, File.Read's value is the buffer Go wrote with its
// count, and ReadAt, whose buffer is dead after it, writes that buffer in
// place. The emitted Go type-checks, and copyFile copies no buffer, as
// hand Go copies none (S2-COPY, the promised form): Read gets buf, which
// `buf = b` rebinds before any other read (killOf); b, the slice Go wrote,
// escapes nowhere, since Go keeps neither the written operand nor the
// operand Write only reads (b[:n], its reads row), so `buf = b` moves b.
// Mutants (lower and check mutants.json): s2-writes-method-skipped,
// s2-writes-result-dropped, s2-writes-results-refused, s2c-kill-off,
// s2c-written-escapes, s2c-reads-escapes, s2c-reads-slice-refused.
func TestFileHandlesNeedOnlyAmbient(t *testing.T) {
	p := lowerSource("hostreadloop", hostReadLoop, true)
	if p.err != nil || len(p.diags) != 0 || len(p.refused) != 0 || p.panicked != nil {
		t.Fatalf("err=%v diagnostics=%v refusals=%v panic=%v\n%s", p.err, p.diags, p.refused, p.panicked, hostReadLoop)
	}
	typecheckGo(t, []*lowered{p})
	goSrc := p.mainGo()
	i := strings.Index(goSrc, "func copyFile(")
	if i < 0 {
		t.Fatalf("no copyFile in the emitted Go:\n%s", goSrc)
	}
	body := goSrc[i:]
	if j := strings.Index(body, "\n}\n"); j >= 0 {
		body = body[:j]
	}
	body = regexp.MustCompile(`/\*line [^*]*\*/`).ReplaceAllString(body, "")
	// ReadAt's buffer is dead after the call, so Go writes it in place: the
	// written temporary is buf itself, and the tuple's first value is it.
	if !regexp.MustCompile(`(?m)^\s*(w_\d+) := buf\n(?:.*\n)*?.*\.ReadAt\(w_\d+, `).MatchString(body) {
		t.Errorf("copyFile's ReadAt does not take buf itself:\n%s", body)
	}
	if strings.Contains(body, "Clone(") {
		t.Errorf("copyFile copies a buffer:\n%s", body)
	}
}

// hostFileMethods calls the rest of *os.File's methods (S2-FILE), each a
// host row, with only Ambient.
const hostFileMethods = `import go os
import go time

func touch(dir string, name string) int fails go.Error needs Ambient {
	let f = try os.OpenFile(name, 2, 420)
	let mut b = []byte{1, 2, 3}
	let n = try f.WriteAt(b, 4)
	b[0] = 9
	try f.Chmod(420)
	try f.Chown(-1, -1)
	try f.SetDeadline(time.Now())
	try f.SetReadDeadline(time.Now())
	try f.SetWriteDeadline(time.Now())
	try f.Close()
	let d = try os.Open(dir)
	let names = try d.Readdirnames(-1)
	try d.Chdir()
	try d.Close()
	n + len(names) + int(b[0])
}

func main() needs Console | Ambient {
	let n = touch("/nonexistent/loop-probe-x", "/nonexistent/loop-probe-y") catch { case go.Error: -1 }
	Console.Println("\(n)")
}
`

// TestFileMethodsNeedOnlyAmbient: WriteAt, Chmod, Chown, Chdir,
// Readdirnames and the three deadlines check with Ambient alone, and the
// emitted Go type-checks; WriteAt only reads b (its reads row), so the
// live b goes to Go uncopied, and Readdirnames' result is new memory (its
// fresh row), so Ergo takes it uncopied. Mutants (gofacts mutants.json):
// s2f-writeat-reads-line-dropped, s2f-readdirnames-fresh-dropped,
// s2f-handle-row-foreign.
func TestFileMethodsNeedOnlyAmbient(t *testing.T) {
	p := lowerSource("hostfilemethods", hostFileMethods, true)
	if p.err != nil || len(p.diags) != 0 || len(p.refused) != 0 || p.panicked != nil {
		t.Fatalf("err=%v diagnostics=%v refusals=%v panic=%v\n%s", p.err, p.diags, p.refused, p.panicked, hostFileMethods)
	}
	typecheckGo(t, []*lowered{p})
	goSrc := p.mainGo()
	i := strings.Index(goSrc, "func touch(")
	if i < 0 {
		t.Fatalf("no touch in the emitted Go:\n%s", goSrc)
	}
	body := goSrc[i:]
	if j := strings.Index(body, "\n}\n"); j >= 0 {
		body = body[:j]
	}
	if strings.Contains(body, "Clone(") {
		t.Errorf("touch copies b for WriteAt or Readdirnames' names:\n%s", regexp.MustCompile(`/\*line [^*]*\*/`).ReplaceAllString(body, ""))
	}
}
