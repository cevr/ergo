// Package genprogram writes neutral generated Ergo programs for the
// compile-speed measurements and their counted tests (compile-speed plan
// §0, CS0): a module of N packages in layers of 8, where each package
// imports up to two packages of the layer below, and a cmd/app main package
// that imports the last packages. Each package has a record, a union, a
// failure type, groups of functions over them and a tests file.
//
// A program is a function of its seed and size: the same seed and size
// give the same files, byte for byte; the seed chooses each package's
// number of function groups, its imports in the layer below and the
// constants in its bodies. scripts/buildcost writes them to disk; tests
// generate them in memory.
package genprogram

import (
	"fmt"
	"math/rand/v2"
	"strings"
)

// Width is the number of packages in a layer.
const Width = 8

// Program is a generated module: its path and its files, by slash path
// relative to the module root (ergo.mod included).
type Program struct {
	Module string
	Files  map[string]string
	// Packages are the import paths of the module's packages, in the order
	// the generator made them, cmd/app last.
	Packages []string
}

// Options size a program beyond its package count.
type Options struct {
	// Groups bounds each package's function groups: the seed draws a
	// number from 1 to Groups. Zero means 4.
	Groups int
}

// Name is the folder and last path element of package i.
func Name(i int) string { return fmt.Sprintf("p%03d", i) }

// GenerateProgram is the program of n packages plus cmd/app for seed.
func GenerateProgram(seed uint64, n int, opts Options) Program {
	if opts.Groups <= 0 {
		opts.Groups = 4
	}
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	mod := fmt.Sprintf("gen%d", n)
	p := Program{Module: mod, Files: map[string]string{"ergo.mod": "module " + mod + "\n"}}
	for i := range n {
		deps := depsOf(r, i)
		groups := 1 + r.IntN(opts.Groups)
		p.Files[Name(i)+"/"+Name(i)+".ergo"] = pkg(r, mod, i, deps, groups)
		p.Files[Name(i)+"/tests.ergo"] = tests(i)
		p.Packages = append(p.Packages, mod+"/"+Name(i))
	}
	p.Files["cmd/app/main.ergo"] = mainFile(mod, n)
	p.Packages = append(p.Packages, mod+"/cmd/app")
	return p
}

// depsOf draws the imports of package i: one or two distinct packages of
// the layer below; none in the first layer.
func depsOf(r *rand.Rand, i int) []int {
	if i < Width {
		return nil
	}
	base := (i/Width - 1) * Width
	a := base + r.IntN(Width)
	if r.IntN(2) == 0 {
		return []int{a}
	}
	b := base + r.IntN(Width)
	if b == a {
		return []int{a}
	}
	return []int{min(a, b), max(a, b)}
}

func pkg(r *rand.Rand, mod string, i int, deps []int, groups int) string {
	var b strings.Builder
	w := func(lines ...string) {
		for _, l := range lines {
			b.WriteString(l)
			b.WriteByte('\n')
		}
	}
	w(fmt.Sprintf("// Package %s is generated: items, shapes and their sums.", Name(i)), "")
	w("import strings")
	for _, d := range deps {
		w("import " + mod + "/" + Name(d))
	}
	w("")
	w("// Item is one row.", "type Item { ID int, Name string, Weight int }", "")
	w("// Shape is a closed set of forms.", "type Shape = Circle{R int} | Rect{W int, H int} | Dot{}", "")
	w("// Bad is a refused input.", "type Bad { Reason string }", "")
	w("// Area is the area of s.", "func Area(s Shape) int {", "\tmatch s {", "\tcase Circle as c:", "\t\t3 * c.R * c.R", "\tcase Rect as r:", "\t\tr.W * r.H", "\tcase Dot:", "\t\t0", "\t}", "}", "")
	for f := range groups {
		k := 1 + r.IntN(1000)
		w(fmt.Sprintf("// Make%d is the items 0 to n-1 of group %d.", f, f))
		w(fmt.Sprintf("func Make%d(n int) []Item {", f), "\tlet mut out = []Item{}", "\tfor j in 0..n {",
			fmt.Sprintf("\t\tout = out.Append(Item{ID: j + %d, Name: \"g%d-\\(j)\", Weight: (j * %d) %% %d})", k, f, k%13+2, k%7+3), "\t}", "\tout", "}", "")
		w(fmt.Sprintf("func shape%d(it Item) Shape {", f), fmt.Sprintf("\tif it.Weight > %d {", k%5), fmt.Sprintf("\t\treturn Rect{W: it.Weight, H: %d}", k%4+1), "\t}", fmt.Sprintf("\tCircle{R: it.ID %% %d}", k%6+2), "}", "")
		w(fmt.Sprintf("// Sum%d is the weight of items, with a shape's area for each heavy one.", f))
		w(fmt.Sprintf("func Sum%d(items []Item) int {", f), "\tlet mut total = 0", "\tfor it in items {", fmt.Sprintf("\t\ttotal = total + Area(shape%d(it)) + it.Weight", f), "\t}")
		if len(deps) > 0 {
			d := Name(deps[f%len(deps)])
			// A dependency may have fewer groups: its group 0 always exists.
			w(fmt.Sprintf("\ttotal + %s.Sum0(%s.Make0(len(items) %% 5))", d, d))
		} else {
			w("\ttotal")
		}
		w("}", "")
		w(fmt.Sprintf("// Pick%d is the item named name, or Bad.", f))
		w(fmt.Sprintf("func Pick%d(items []Item, name string) Item fails Bad {", f), "\tfor it in items {", "\t\tif it.Name == name {", "\t\t\treturn it", "\t\t}", "\t}", fmt.Sprintf("\tBad{Reason: \"group %d has no \" + name}", f), "}", "")
		sep := ","
		if f%2 == 1 {
			sep = ";"
		}
		w(fmt.Sprintf("// Text%d is one line for each item.", f))
		w(fmt.Sprintf("func Text%d(items []Item) string {", f), "\tlet mut parts = []string{}", "\tfor it in items {", "\t\tparts = parts.Append(\"\\(it.ID):\\(it.Name)=\\(it.Weight)\")", "\t}", fmt.Sprintf("\tstrings.Join(parts, %q)", sep), "}", "")
	}
	w("// Total is every group's sum at size n.", "func Total(n int) int {", "\tlet mut t = 0")
	for f := range groups {
		w(fmt.Sprintf("\tt = t + Sum%d(Make%d(n))", f, f))
	}
	// Every import is used, whatever the groups drew.
	for _, d := range deps {
		w(fmt.Sprintf("\tt = t + len(%s.Make0(0))", Name(d)))
	}
	w("\tt", "}")
	return b.String()
}

func tests(i int) string {
	return fmt.Sprintf(`// The tests of %s.

test TotalIsStable {
	assert Total(3) == Total(3)
}

test PickFindsOrFails {
	let items = Make0(4)
	let got = Pick0(items, "g0-2") catch {
	case Bad:
		assert false
		return
	}
	assert got.Name == "g0-2"
}
`, Name(i))
}

func mainFile(mod string, n int) string {
	var last []int
	for i := max(0, n-2*Width); i < n; i++ {
		last = append(last, i)
	}
	var b strings.Builder
	b.WriteString("// app prints the total of the generated packages.\n\n")
	for _, i := range last {
		b.WriteString("import " + mod + "/" + Name(i) + "\n")
	}
	b.WriteString("import io\nimport os\n\nfunc total(n int) int {\n\tlet mut t = 0\n")
	for _, i := range last {
		fmt.Fprintf(&b, "\tt = t + %s.Total(n)\n", Name(i))
	}
	b.WriteString("\tt\n}\n\n")
	b.WriteString("func main() fails os.Exit needs Ambient {\n\twith context { io.Stdout: io.NewHost() } {\n\t\tio.Stdout.Println(\"total \\(total(3))\")\n\t}\n}\n\n")
	b.WriteString("test TotalRuns {\n\tassert total(2) == total(2)\n}\n")
	return b.String()
}
