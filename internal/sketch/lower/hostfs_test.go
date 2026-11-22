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

// TestHostFSProgramRuns is HOST-ROW-3's run: a program that calls os.Stat,
// os.Lstat and os.ReadDir, which os's binding sends to the toolchain's Go
// package runtime/v1/hostfs (gobind.ValueResults), builds and prints the
// fields it reads.
//
// It builds Go because the question is whether the emitted module holds
// hostfs and lets main.go import it (module.go Runtime, calls.go hostPkg):
// only go build resolves the import in the emitted tree; the checker and
// the lowered text cannot see a missing package.
//
// Mutant it alone catches: hostfs-not-copied (the emitted module lacks
// hostfs, so main.go's import of it does not resolve).
func TestHostFSProgramRuns(t *testing.T) {
	needsBuild(t)
	dir := t.TempDir()
	src := `import go os

func main() needs Console | Ambient {
	let info = os.Stat("data.txt") catch {
	case go.Error:
		return
	}
	let link = os.Lstat("data.txt") catch {
	case go.Error:
		return
	}
	Console.Println("\(info.Name) \(info.Size) \(info.IsDir) \(uint32(link.Mode) & 511)")
	let es = os.ReadDir("sub") catch {
	case go.Error:
		return
	}
	for e in es {
		Console.Println("\(e.Name) \(e.IsDir)")
	}
}
`
	for name, data := range map[string]string{"main.ergo": src, "data.txt": "four"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub", "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "a.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(ergoCLI(t), "run", "main.ergo")
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := runProgram(t, cmd); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	got := out.String()
	if note, rest, ok := strings.Cut(got, "\n"); ok && strings.HasPrefix(note, toolchain.BuildNote) {
		got = rest
	}
	if want := "data.txt 4 false 416\na.txt false\ninner true\n"; got != want {
		t.Errorf("the program prints:\n%s\nwant:\n%s", got, want)
	}
}
