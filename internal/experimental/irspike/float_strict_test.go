package irspike

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// floatStrictProgram computes float products in each form an IR body
// writes one (a sum's operand, a product bound and summed later, x *= v,
// p.x *= v), the sum after the product being what arm64 fuses.
const floatStrictProgram = `type P { x float64 }

func mad(a float64, b float64, c float64) float64 {
	a * b + c
}

func later(a float64, b float64, c float64) float64 {
	let t = a * b
	t + c
}

func scaled(a float64, b float64, c float64) float64 {
	let mut x = a
	x *= b
	x + c
}

func field(a float64, b float64) float64 {
	let mut p = P{x: a}
	p.x *= b
	p.x + a
}

func main() needs Console {
	Console.Println("\(mad(1.1, 2.2, 3.3)) \(later(1.1, 2.2, 3.3)) \(scaled(1.1, 2.2, 3.3)) \(field(1.1, 2.2))")
}
`

// TestIRFloatsUnfusedOnArm64 is FLOAT-STRICT's rule where it matters: the
// IR build of a float program for darwin/arm64, the Mac's target, holds no
// fused multiply-add (FMADDD, FMSUBD, FNMADDD, FNMSUBD) in its functions,
// and holds their products (FMULD), so a float result has the same bits
// as on amd64, which never fuses. Go 1.27.4 fuses t := a*b; t + c into
// one FMADDD on arm64 (measured, FLOAT-STRICT request); the IR's T(a * b)
// stops it.
//
// It builds Go because the question is what gc's arm64 back end emits for
// the decoded IR: an explicit conversion in the IR must reach SSA as a
// rounding, which no in-process check of the IR shows
// (TestIRFloatProductsRound checks the IR itself).
func TestIRFloatsUnfusedOnArm64(t *testing.T) {
	_, ir := needsBuild(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "p.ergo")
	if err := os.WriteFile(file, []byte(floatStrictProgram), 0o644); err != nil {
		t.Fatal(err)
	}
	u, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	files, rep, err := lower.LowerIR(u)
	if err != nil {
		t.Fatal(err)
	}
	mod := filepath.Join(dir, "ir")
	if err := writeModule(mod, files); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	cmd, err := Command(ir, IR, mod, filepath.Join(dir, "p.arm64"), BuildOptions{GCFlags: []string{"-S"}, Stderr: &out})
	if err != nil {
		t.Fatal(err)
	}
	cmd.Env = append(cmd.Env, "GOOS=darwin", "GOARCH=arm64")
	if err := cmd.Run(); err != nil {
		t.Fatalf("the arm64 IR build: %v\n%s", err, out.String())
	}
	fused := regexp.MustCompile(`\bF(N?)M(ADD|SUB)D\b`)
	for _, fn := range []string{"mad", "later", "scaled", "field"} {
		asm := funcAsm(out.String(), "main."+fn)
		if asm == "" {
			t.Errorf("%s: no assembly (IR bodies %v, fallback %v)", fn, rep.IR, rep.Fallback)
			continue
		}
		if m := fused.FindString(asm); m != "" || !strings.Contains(asm, "FMULD") {
			t.Errorf("%s: arm64 code with a fused %q, or no FMULD:\n%s", fn, m, asm)
		}
	}
}

// funcAsm is the -S listing of the function name in asm, up to the next
// symbol.
func funcAsm(asm, name string) string {
	var b strings.Builder
	in := false
	for _, line := range strings.Split(asm, "\n") {
		if !strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, " ") {
			in = strings.HasPrefix(line, name+" STEXT")
		}
		if in {
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}
