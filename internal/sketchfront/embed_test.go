package sketchfront

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestEmbeddedAssetsSnapshot catches missing directives, dropped payloads
// and stale asset build identity (testdata/mutants.json). It checks both
// build modes in process: a native compiler, not Ergo, stores the payload
// in the binary. Go source size must not grow with that payload.
func TestEmbeddedAssetsSnapshot(t *testing.T) {
	seed := uint64(71)
	if s := os.Getenv("ERGO_EMBED_SEED"); s != "" {
		var err error
		seed, err = strconv.ParseUint(s, 0, 64)
		if err != nil {
			t.Fatal(err)
		}
	}
	rng := rand.New(rand.NewPCG(seed, seed+1))
	for _, kind := range []string{"string", "[]byte", "embed.FS"} {
		for _, location := range []string{"root", "imported"} {
			t.Run(kind+"/"+location, func(t *testing.T) {
				imports := ""
				pattern := "data/help.txt"
				if kind == "embed.FS" {
					imports = "import embed\n"
					pattern = "data"
				}
				source := imports + fmt.Sprintf("let Asset = embedded[%s](%q)\n", kind, pattern)
				payload := make([]byte, 97+rng.IntN(4096))
				for i := range payload {
					payload[i] = byte(rng.UintN(256))
				}
				files := map[string]string{"ergo.mod": "module app\n", "main.ergo": source + "func main() {}\n", "data/help.txt": string(payload)}
				assetPath := "data/help.txt"
				if location == "imported" {
					files = map[string]string{"ergo.mod": "module app\n", "main.ergo": "import app/lib\nfunc main() { lib.Use() }\n", "lib/lib.ergo": source + "func Use() {}\n", "lib/data/help.txt": string(payload)}
					assetPath = "lib/data/help.txt"
				}
				root := writeTree(t, files)
				var beforeText, beforeGo string
				var beforeDependencies string
				for edit := 0; edit < 2; edit++ {
					a, err := CheckDir(root)
					if err != nil || len(a.Diagnostics) > 0 {
						t.Fatalf("seed %d; ERGO_EMBED_SEED=%d go test -run TestEmbeddedAssetsSnapshot ./internal/sketchfront\n%s\n%v\n%s", seed, seed, source, err, diagnosticLines(a))
					}
					for mode, lowerMode := range []func(lower.Unit) ([]lower.File, error){lower.Lower, lower.LowerTests} {
						files, err := lowerMode(a.program.unit())
						if err != nil {
							t.Fatalf("seed %d\n%s\n%v", seed, source, err)
						}
						var code []byte
						var assetBytes int
						for _, f := range files {
							if f.Path == "main.go" {
								code = f.Data
							}
							if strings.HasPrefix(f.Path, "assets/") {
								if path.Base(f.Path) != "help.txt" || !bytes.Equal(f.Data, payload) {
									t.Fatalf("seed %d: asset %s bytes differ: got %x, want %x", seed, f.Path, f.Data, payload)
								}
								assetBytes += len(f.Data)
							}
						}
						if assetBytes != len(payload) || !bytes.Contains(code, []byte("//go:embed ")) || bytes.Contains(code, payload) {
							t.Fatalf("seed %d: payload must occur once in asset files and never in Go literals; asset bytes %d want %d\n%s", seed, assetBytes, len(payload), code)
						}
						if mode == 0 {
							if edit == 0 {
								beforeGo = string(code)
							} else if string(code) != beforeGo {
								t.Fatal("asset-only edit changed Go source; payload belongs in go:embed inputs")
							}
						}
					}
					if edit == 0 {
						beforeText = a.Text()
						beforeDependencies = fmt.Sprint(a.program.dependencies())
						payload = append(payload, byte(rng.UintN(256)))
						if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(assetPath)), payload, 0o644); err != nil {
							t.Fatal(err)
						}
					} else if a.Text() == beforeText || fmt.Sprint(a.program.dependencies()) == beforeDependencies {
						t.Fatal("an asset edit did not change the analysis revision and build dependencies")
					}
				}
			})
		}
	}
}

// TestEmbeddedDiagnostics catches package escapes, local initialization and
// multi-file scalar embeds. Concrete cases earn their text and position.
func TestEmbeddedDiagnostics(t *testing.T) {
	for _, row := range []struct{ source, want string }{
		{`let Asset = embedded[string]("../outside")`, "must stay under the package folder"},
		{`let Asset = embedded[string]("/nonexistent/loop-probe-x")`, "must stay under the package folder"},
		{`let Asset = embedded[string]("missing")`, "matches no files"},
		{`let Asset = embedded[string]("link")`, "symbolic link"},
		{`let Asset = embedded[string]("data/*")`, "exactly one file"},
		{`func f() string { embedded[string]("data/a") }`, "whole initializer"},
		{`let Asset = embedded[int]("data/a")`, "string, []byte or embed.FS"},
		{`func pattern() string { "data/a" }; let Asset = embedded[string](pattern())`, "constant string"},
	} {
		t.Run(row.want+row.source, func(t *testing.T) {
			root := writeTree(t, map[string]string{"main.ergo": row.source + "\nfunc main() {}\n", "data/a": "a", "data/b": "b"})
			if err := os.Symlink(filepath.Join(root, "data/a"), filepath.Join(root, "link")); err != nil {
				t.Fatal(err)
			}
			a, err := CheckDir(root)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, d := range a.Diagnostics {
				if d.Code == "S108" && strings.Contains(d.Message, row.want) && d.File == "main.ergo" && d.Span.Line == 1 && d.Span.Column > 0 {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s\nwant positioned S108 %s, got\n%s", row.source, row.want, diagnosticLines(a))
			}
		})
	}
}
