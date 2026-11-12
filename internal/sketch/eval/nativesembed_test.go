package eval

import (
	"fmt"
	"math/rand/v2"
	"path"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// assets is a checker importer with a seeded asset tree: std's packages,
// and each embedded pattern's files under it.
type assets map[string][]byte

func (assets) Import(p string) (*check.Package, error) { return check.Std.Import(p) }

func (a assets) ResolveEmbed(_ *syntax.File, pattern string, tree bool) ([]check.EmbedFile, error) {
	var out []check.EmbedFile
	for p, data := range a {
		if p == pattern || tree && strings.HasPrefix(p, pattern+"/") {
			out = append(out, check.EmbedFile{Path: p, Data: data})
		}
	}
	slices.SortFunc(out, func(x, y check.EmbedFile) int { return strings.Compare(x.Path, y.Path) })
	if len(out) == 0 {
		return nil, fmt.Errorf("no file matches %s", pattern)
	}
	return out, nil
}

// TestEmbedAgreesWithModel holds embedded[T] (nativesembed.go) to Go's
// embed rules on seeded asset trees, under RunMain: a string and a []byte
// are the file's bytes; an embed.FS reads a file's text, lists a
// directory's names sorted, and stats a file (mode 0444) and a directory
// (0555, size 0), and every other name, a directory read as a file
// included, fails fs.NotFound with that name. The model is this test's
// own reading of io/fs and Go's embed.FS, not the evaluator's tree.
//
// Mutants only it kills (testdata/mutants.json): oracle-o15-bytes-as-text,
// oracle-o15-file-mode, oracle-o15-dir-read-as-file.
func TestEmbedAgreesWithModel(t *testing.T) {
	for seed := uint64(1); seed <= 20; seed++ {
		r := rand.New(rand.NewPCG(seed, 15))
		dirs := []string{"a", "a/b", "a/c", "a/b/d"}
		names := []string{"x.txt", "y", "z.md"}
		tree := assets{"a/x.txt": []byte("text" + fmt.Sprint(seed))}
		for range 1 + r.IntN(5) {
			p := dirs[r.IntN(len(dirs))] + "/" + names[r.IntN(len(names))]
			tree[p] = []byte(strings.Repeat(string(rune('a'+r.IntN(26))), 1+r.IntN(5)))
		}
		// The directories the files make, "a" first.
		isDir := map[string]bool{}
		for p := range tree {
			for d := path.Dir(p); d != "."; d = path.Dir(d) {
				isDir[d] = true
			}
		}
		queries := []string{"a", "a/nope", "a/x.txt"}
		for range 4 {
			pool := append(append([]string{}, dirs...), "a/b/y", "a/c/x.txt", "a/z.md")
			queries = append(queries, pool[r.IntN(len(pool))])
		}
		var src, want strings.Builder
		src.WriteString(`import embed
import fs
import strings

let Tree = embedded[embed.FS]("a")
let Text = embedded[string]("a/x.txt")
let Raw = embedded[[]byte]("a/x.txt")

func show(p string) needs Console {
	let data = Tree.ReadFile(p) catch {
	case fs.NotFound as e:
		Console.Println("read missing \(e.Path)")
		return
	case fs.Denied, fs.Failed:
		Console.Println("read refused")
		return
	}
	Console.Println("read \(p) \(data)")
}

func list(p string) needs Console {
	let names = Tree.ReadDir(p) catch {
	case fs.NotFound as e:
		Console.Println("list missing \(e.Path)")
		return
	case fs.Denied, fs.Failed:
		Console.Println("list refused")
		return
	}
	Console.Println("list \(p) " + strings.Join(names, ","))
}

func stat(p string) needs Console {
	let i = Tree.Stat(p) catch {
	case fs.NotFound as e:
		Console.Println("stat missing \(e.Path)")
		return
	case fs.Denied, fs.Failed:
		Console.Println("stat refused")
		return
	}
	Console.Println("stat \(i.Name) \(i.Size) \(i.Dir) \(i.Perm)")
}

func main() needs Console {
	Console.Println("text \(Text) \(len(Raw)) \(int(Raw[0]))")
`)
		text := tree["a/x.txt"]
		fmt.Fprintf(&want, "text %s %d %d\n", text, len(text), int(text[0]))
		for _, q := range queries {
			fmt.Fprintf(&src, "\tshow(%q)\n\tlist(%q)\n\tstat(%q)\n", q, q, q)
			data, file := tree[q]
			switch {
			case file:
				fmt.Fprintf(&want, "read %s %s\n", q, data)
			default:
				fmt.Fprintf(&want, "read missing %s\n", q)
			}
			if isDir[q] {
				var kids []string
				for p := range tree {
					for c := p; c != "."; c = path.Dir(c) {
						if path.Dir(c) == q && !slices.Contains(kids, path.Base(c)) {
							kids = append(kids, path.Base(c))
						}
					}
				}
				slices.Sort(kids)
				fmt.Fprintf(&want, "list %s %s\n", q, strings.Join(kids, ","))
			} else {
				fmt.Fprintf(&want, "list missing %s\n", q)
			}
			switch {
			case file:
				fmt.Fprintf(&want, "stat %s %d false %d\n", path.Base(q), len(data), 0o444)
			case isDir[q]:
				fmt.Fprintf(&want, "stat %s 0 true %d\n", path.Base(q), 0o555)
			default:
				fmt.Fprintf(&want, "stat missing %s\n", q)
			}
		}
		src.WriteString("}\n")
		f, err := syntax.Parse(src.String())
		if err != nil {
			t.Fatal(err)
		}
		pkg, diags := check.Check("main", []*syntax.File{f}, tree)
		if len(diags) > 0 {
			t.Fatalf("seed %d: %v\n%s", seed, diags, src.String())
		}
		out, err := RunMain(pkg, DefaultBudget(), Process{})
		if err != nil {
			t.Fatal(err)
		}
		if out.Skipped != "" || out.Stdout != want.String() {
			t.Fatalf("seed %d: the evaluator gives\n%s(skipped %q)\nthe model\n%s\nsource:\n%s", seed, out.Stdout, out.Skipped, want.String(), src.String())
		}
	}
}
