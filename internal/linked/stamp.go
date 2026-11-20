package linked

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// StampFile is the stamp `go run ./scripts/linked` writes after the
// generation, by path from the repository root: the digest of the
// generation's inputs (InputsDigest) on its first line, then one line
// "<sha256> <path>" per file the generation wrote. It makes the check of
// the generated files cheap (Fresh): while the inputs digest still holds,
// the generator would write what the stamp lists, so the checked-in files
// are fresh when their bytes hash as listed, and nothing regenerates.
const StampFile = "internal/linked/generated.sum"

// InputsDigest is the digest of everything the generation reads: the Go
// version, go.mod, the source files (Go, assembly and embedded files) of
// every module package the generator's import closure holds, which
// includes the compiler, the lowering and the embedded std, and every
// file under each package's Ergo module (internal/linked/<name>/ergo).
// A miss costs a regeneration, never a wrong verdict: any input change
// changes it.
func InputsDigest(root string) (string, error) {
	cmd := exec.Command("go", "list", "-deps", "-f",
		`{{if not .Standard}}{{.Dir}}{{range .GoFiles}} {{.}}{{end}}{{range .CgoFiles}} {{.}}{{end}}{{range .SFiles}} {{.}}{{end}}{{range .EmbedFiles}} {{.}}{{end}}{{end}}`,
		Module+"/internal/linked")
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("go list the generator's closure: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	var files []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(nil, 1<<24)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 {
			continue
		}
		for _, f := range fields[1:] {
			files = append(files, filepath.Join(fields[0], f))
		}
	}
	files = append(files, filepath.Join(root, "go.mod"))
	for _, p := range Packages {
		// Its Ergo module: internal/linked/<name>/ergo, or its Source
		// (the self-hosted compiler's module, compiler/).
		dir := filepath.Join(root, filepath.FromSlash(p.ModuleDir()))
		err := filepath.WalkDir(dir, func(f string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type().IsRegular() {
				files = append(files, f)
			}
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	slices.Sort(files)
	h := sha256.New()
	fmt.Fprintf(h, "%s\n", runtime.Version())
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return "", err
		}
		rel, err := filepath.Rel(root, f)
		if err != nil {
			return "", err
		}
		sum := sha256.Sum256(data)
		fmt.Fprintf(h, "%s %s\n", hex.EncodeToString(sum[:]), filepath.ToSlash(rel))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Stamp is StampFile's content for a generation with the inputs digest
// inputs that wrote files.
func Stamp(inputs string, files map[string][]byte) []byte {
	var b bytes.Buffer
	b.WriteString(inputs + "\n")
	for _, rel := range Sorted(files) {
		sum := sha256.Sum256(files[rel])
		fmt.Fprintf(&b, "%s %s\n", hex.EncodeToString(sum[:]), rel)
	}
	return b.Bytes()
}

// Fresh says whether the stamp vouches for the checked-in generated files:
// its inputs digest is inputs, and each file it lists reads (read, by path
// from the repository root) with the bytes it hashed. On false the caller
// regenerates and compares (TestLinkedGoIsFresh): a false is a miss, not
// a stale verdict.
func Fresh(stamp []byte, inputs string, read func(rel string) ([]byte, error)) bool {
	lines := strings.Split(strings.TrimSuffix(string(stamp), "\n"), "\n")
	if len(lines) < 2 || lines[0] != inputs {
		return false
	}
	for _, line := range lines[1:] {
		want, rel, ok := strings.Cut(line, " ")
		if !ok || path.Clean(rel) != rel {
			return false
		}
		data, err := read(rel)
		if err != nil {
			return false
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != want {
			return false
		}
	}
	return true
}
