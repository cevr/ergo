package proto

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// base declares the types most cases share.
const base = `import timeout

type NotFound { ID string }
type Down {}

type Store {
	Get(id string) string fails NotFound,
}

type Net {
	Send(msg string) fails Down,
}

type MemStore {}

func Get(_ MemStore, id string) string fails NotFound {
	if id == "1" {
		return "one"
	}
	return NotFound{ID: id}
}

type OkNet {}

func Send(_ OkNet, msg string) fails Down {
	return
}
`

// both fails with two errors, so a catch can handle one and pass the other on.
const both = `
func both(id string) string fails NotFound | Down needs Store | Net {
	let v = try Store.Get(id)
	try Net.Send(v)
	return v
}`

func codes(t *testing.T, src string) []string {
	t.Helper()
	f, err := Parse("demo.ergo", src)
	if err != nil {
		return []string{strings.Fields(strings.SplitN(err.Error(), ": ", 2)[1])[0]}
	}
	_, diags := Check(f)
	var out []string
	for _, d := range diags {
		out = append(out, d.Code)
	}
	return out
}

func TestCheck(t *testing.T) {
	cases := []struct {
		name, body string
		want       string // "" means accepted
	}{
		{"accepts try under a declared row", `
func get(id string) string fails NotFound needs Store {
	let v = try Store.Get(id)
	return v
}
func main() needs Console { Console.Println("x") }`, ""},
		{"partial catch under try passes the rest on", both + `
func partial(id string) string fails Down needs Store | Net {
	let v = try both(id) catch {
	case NotFound:
		"none"
	}
	return v
}
func main() needs Console { Console.Println("x") }`, ""},
		{"partial catch under try still needs the rest declared", both + `
func partial(id string) string fails NotFound needs Store | Net {
	let v = try both(id) catch {
	case NotFound:
		"none"
	}
	return v
}
func main() needs Console { Console.Println("x") }`, "S101"},
		{"an unhandled fallible call", `
func get(id string) string needs Store {
	let v = Store.Get(id)
	return v
}
func main() needs Console { Console.Println("x") }`, "S105"},
		{"a provider without the method", `
type Empty {}
func main() needs Console {
	let v = with context { Store: Empty{} } { "x" }
	Console.Println(v)
}`, "S106"},
		{"a provider whose row is wider", `
type Loose {}
func Get(_ Loose, id string) string fails NotFound | Down { return "x" }
func main() needs Console {
	let v = with context { Store: Loose{} } { "x" }
	Console.Println(v)
}`, "S106"},
		{"a missing return", `
func pick(id string) string {
	if id == "1" {
		return "one"
	}
}
func main() needs Console { Console.Println(pick("1")) }`, "S107"},
		{"an unused variable", `
func main() needs Console {
	let x = "unused"
	Console.Println("y")
}`, "S110"},
		{"timeout requires Scheduler", `
func slow(id string) string fails NotFound | timeout.Expired needs Store {
	let v = try timeout.After(1s) { Store.Get(id) }
	return v
}
func main() needs Console { Console.Println("x") }`, "S102"},
		{"main cannot require what the host lacks", `
func main() needs Console | Store { Console.Println("x") }`, "S102"},
		{"a reserved name", `
func main() needs Console {
	let string = "x"
	Console.Println(string)
}`, "S109"},
		{"a parse error", `
func main() needs Console { for }`, "S001"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(codes(t, base+tc.body), " ")
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestBuildAndRun builds a second program through go build: a partial catch
// under try, a function with no result, a with nested in another, and a
// timeout that the body beats.
func TestBuildAndRun(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go tool")
	}
	src := base + `
func lookup(id string) string fails Down | timeout.Expired needs Store | Net | Scheduler {
	let v = try timeout.After(1s) { Store.Get(id) } catch {
	case NotFound:
		"missing " + id
	}
	try Net.Send(v)
	return v
}

func show(id string) needs Console | Store | Net | Scheduler {
	let v = lookup(id) catch {
	case Down:
		"down"
	case timeout.Expired:
		"slow"
	}
	Console.Println(v)
}

func main() needs Console {
	with context { Store: MemStore{}, Scheduler: LiveScheduler{} } {
		with context { Net: OkNet{} } {
			show("1")
			show("2")
		}
	}
}
`
	out := buildRun(t, src)
	if got, want := out, "one\nmissing 2\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// buildRun checks, emits and runs src through go run, and returns its output.
func buildRun(t *testing.T, src string) string {
	t.Helper()
	f, err := Parse("demo.ergo", src)
	if err != nil {
		t.Fatal(err)
	}
	c, diags := Check(f)
	if len(diags) > 0 {
		t.Fatalf("diagnostics: %v", diags)
	}
	code, err := Emit(c)
	if err != nil {
		t.Fatalf("%v\n%s", err, code)
	}
	dir := t.TempDir()
	for name, body := range map[string]string{"go.mod": GoMod, "main.go": code, "rt/rt.go": RuntimeSource()} {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	vet := exec.Command("go", "vet", ".")
	vet.Dir = dir
	if out, err := vet.CombinedOutput(); err != nil {
		t.Fatalf("go vet: %v\n%s\n%s", err, out, code)
	}
	cmd := exec.Command("go", "run", ".")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run: %v\n%s\n%s", err, out, code)
	}
	return string(out)
}

// TestProbes runs the review witnesses in testdata/probes. The first line of
// each says what it must do: "// want CODE" (rejected with that code),
// "// out: a|b" (builds, passes go vet, and prints those lines), or
// "// out-quoted: "..."" (the same, with the output as a Go string literal).
func TestProbes(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go tool")
	}
	files, err := filepath.Glob("testdata/probes/*.ergo")
	if err != nil || len(files) == 0 {
		t.Fatalf("no probes: %v", err)
	}
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			t.Parallel()
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			src := string(raw)
			head, _, _ := strings.Cut(src, "\n")
			switch {
			case strings.HasPrefix(head, "// want "):
				want := strings.TrimPrefix(head, "// want ")
				got := codes(t, src)
				if len(got) == 0 || !slices.Contains(got, want) {
					t.Fatalf("got %v, want %s", got, want)
				}
			case strings.HasPrefix(head, "// out-quoted: "):
				// For output with bytes a header cannot spell, as a Go string literal.
				want, err := strconv.Unquote(strings.TrimPrefix(head, "// out-quoted: "))
				if err != nil {
					t.Fatal(err)
				}
				if got := buildRun(t, src); got != want {
					t.Fatalf("got %q, want %q", got, want)
				}
			case head == "// out:" || strings.HasPrefix(head, "// out: "):
				// The formatter drops the trailing space of "// out: " (no output).
				want := strings.ReplaceAll(strings.TrimPrefix(strings.TrimPrefix(head, "// out:"), " "), "|", "\n")
				if want != "" {
					want += "\n"
				}
				if got := buildRun(t, src); got != want {
					t.Fatalf("got %q, want %q", got, want)
				}
			default:
				t.Fatalf("first line must be // want CODE or // out: lines")
			}
		})
	}
}
