package lower

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/toolchain"
)

const irHostTestGo = `package hostpkg

import "fmt"

type Item struct {
	Tag  string
	Data []byte
}

func MutateSlice(xs []Item) int {
	if len(xs) > 0 {
		xs[0].Tag = "clobbered"
		if len(xs[0].Data) > 0 {
			xs[0].Data[0] = 'X'
		}
	}
	return len(xs)
}

func MutateItem(x Item) int {
	if len(x.Data) > 0 {
		x.Data[0] = 'Y'
	}
	return len(x.Data)
}

type ExportedOnly struct {
	Name  string
	Count int
}

type MixedOpts struct {
	Name   string
	Count  int
	secret int
	tag    string
}

type OpaqueOpts struct {
	secret int
}

func CheckExported(e ExportedOnly) string {
	return fmt.Sprintf("%s:%d", e.Name, e.Count)
}

func CheckMixed(m MixedOpts) string {
	return fmt.Sprintf("%s:%d:%d:%s", m.Name, m.Count, m.secret, m.tag)
}

func CheckOpaque(o OpaqueOpts) int {
	return o.secret
}

func MakeMixedWithSecret(name string, secret int, tag string) MixedOpts {
	return MixedOpts{Name: name, Count: 10, secret: secret, tag: tag}
}
`

func runIRHostModule(t *testing.T, mainSrc string) string {
	t.Helper()
	needsBuild(t)
	dir := t.TempDir()
	const modPath = "example.com/irhosttest"
	const pkgPath = modPath + "/hostpkg"
	if err := os.MkdirAll(filepath.Join(dir, "hostpkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"ergo.mod":           "module " + modPath + "\n",
		"go.mod":             "module " + modPath + "\n\ngo " + goVersion + "\n",
		"hostpkg/hostpkg.go": irHostTestGo,
		"main.ergo":          mainSrc,
	}
	for rel, data := range files {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cli := ergoCLI(t)
	bind := exec.Command(cli, "bind", "go", pkgPath)
	bind.Dir = dir
	var bindOut bytes.Buffer
	bind.Stdout, bind.Stderr = &bindOut, &bindOut
	if err := runProgram(t, bind); err != nil {
		t.Fatalf("ergo bind %s: %v\n%s", pkgPath, err, bindOut.String())
	}
	run := exec.Command(cli, "run", ".")
	run.Dir = dir
	run.Env = append(os.Environ(), "ERGO_FALLBACK=off")
	var out bytes.Buffer
	run.Stdout, run.Stderr = &out, &out
	if err := runProgram(t, run); err != nil {
		t.Fatalf("ergo run (ERGO_FALLBACK=off): %v\n%s", err, out.String())
	}
	got := out.String()
	if note, rest, ok := strings.Cut(got, "\n"); ok && strings.HasPrefix(note, toolchain.BuildNote) {
		got = rest
	}
	return got
}

// TestIRGoCallHostSliceCopy verifies that Go calls taking slices of Go host
// structs ([]hostpkg.Item) and Go host structs holding slices (hostpkg.Item)
// lower via IR under ERGO_FALLBACK=off and deep-copy live operands at the Go
// boundary so a mutating Go callee cannot modify the caller's slice or nested
// []byte.
//
// Mutants (testdata/mutants.json): irgc-host-slice-arg-refused,
// irgc-host-struct-arg-uncopied.
func TestIRGoCallHostSliceCopy(t *testing.T) {
	const src = `import go example.com/irhosttest/hostpkg

func checkSlice(xs []hostpkg.Item) string needs go.Foreign {
	let n = hostpkg.MutateSlice(xs)
	"\(n):\(xs[0].Tag):\(int(xs[0].Data[0]))"
}

func checkItem(x hostpkg.Item) string needs go.Foreign {
	let n = hostpkg.MutateItem(x)
	"\(n):\(x.Tag):\(int(x.Data[0]))"
}

func checkLitSlice(x hostpkg.Item) string needs go.Foreign {
	let n = hostpkg.MutateSlice([]hostpkg.Item{x})
	"\(n):\(x.Tag):\(int(x.Data[0]))"
}

func main() needs Console | go.Foreign {
	let item = hostpkg.Item{Tag: "orig", Data: []byte{65, 66, 67}}
	let xs = []hostpkg.Item{item}
	Console.Println(checkSlice(xs))
	Console.Println(checkItem(item))
	Console.Println(checkLitSlice(item))
}
`
	got := runIRHostModule(t, src)
	want := "1:orig:65\n3:orig:65\n1:orig:65\n"
	if got != want {
		t.Fatalf("output:\n%s\nwant:\n%s", got, want)
	}
}

// TestIRHostStructLiteralWithUnexportedFields verifies that constructing and
// updating Go host structs whose underlying Go struct has unexported fields
// (exported-only, zero-exported-fields, mixed exported+unexported, and record
// update T{..base, F: v}) compile and run via IR under ERGO_FALLBACK=off,
// preserving unexported fields on update.
//
// Mutants (testdata/mutants.json): irgc-host-struct-lit-uses-lit,
// irgc-host-struct-update-uses-lit.
func TestIRHostStructLiteralWithUnexportedFields(t *testing.T) {
	const src = `import go example.com/irhosttest/hostpkg

func buildAll() string needs go.Foreign {
	let e = hostpkg.ExportedOnly{Name: "exp", Count: 3}
	let o = hostpkg.OpaqueOpts
	let f = hostpkg.MixedOpts{Name: "full", Count: 7}
	let z = go.Zero[hostpkg.MixedOpts]()
	let p = hostpkg.MixedOpts{..z, Name: "part"}
	let base = hostpkg.MakeMixedWithSecret("init", 42, "hidden")
	let u = hostpkg.MixedOpts{..base, Name: "updated"}
	"\(hostpkg.CheckExported(e))|\(hostpkg.CheckOpaque(o))|\(hostpkg.CheckMixed(f))|\(hostpkg.CheckMixed(p))|\(hostpkg.CheckMixed(u))"
}

func main() needs Console | go.Foreign {
	Console.Println(buildAll())
}
`
	got := runIRHostModule(t, src)
	want := "exp:3|0|full:7:0:|part:0:0:|updated:10:42:hidden\n"
	if got != want {
		t.Fatalf("output:\n%s\nwant:\n%s", got, want)
	}
}
