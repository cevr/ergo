package sketchfront

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestGoModMatchesGo holds the loader's go.mod reading (gomod.go) to the
// go command's own parser: for seeded go.mod texts in every form the
// loader reads (single lines and blocks, comments, quoted words, replaces
// with and without versions, of local folders and of other modules), the
// requirements and replacements parseGoMod reads equal what `go mod edit
// -json` reads, and moduleKey's stamp for each required module is the one
// a reference written from Go's replacement rule gives (a replacement of
// the exact version wins over one of every version). A failing seed prints
// the go.mod and both readings; ERGO_GOMOD_SEED replays one seed.
//
// Mutants it alone catches: a block form not read; a // comment kept as a
// word; a quoted word not unquoted; the old version of a replacement
// dropped; a replacement of another version applied; the replacement's
// own version left out of the stamp. It runs go mod edit, which reads one
// file and builds nothing.
func TestGoModMatchesGo(t *testing.T) {
	seeds := 120
	first := uint64(1)
	if s := os.Getenv("ERGO_GOMOD_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		first, seeds = n, 1
	}
	dir := t.TempDir()
	for seed := first; seed < first+uint64(seeds); seed++ {
		text, mods := genGoMod(rand.New(rand.NewPCG(seed, 0x60d)))
		name := filepath.Join(dir, "go.mod")
		if err := os.WriteFile(name, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("go", "mod", "edit", "-json", name)
		cmd.Env = append(os.Environ(), "GOFLAGS=", "GOWORK=off")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("seed %d: go mod edit: %v\n%s", seed, err, text)
		}
		var ref struct {
			Require []struct{ Path, Version string }
			Replace []struct {
				Old, New struct{ Path, Version string }
			}
		}
		if err := json.Unmarshal(out, &ref); err != nil {
			t.Fatal(err)
		}
		m, err := parseGoMod(text)
		if err != nil {
			t.Fatalf("seed %d: %v\n%s", seed, err, text)
		}
		var want, got []string
		for _, r := range ref.Require {
			want = append(want, "require "+r.Path+" "+r.Version)
		}
		for _, r := range ref.Replace {
			want = append(want, fmt.Sprintf("replace %s %s => %s %s", r.Old.Path, r.Old.Version, r.New.Path, r.New.Version))
		}
		for _, r := range m.require {
			got = append(got, "require "+r.path+" "+r.version)
		}
		for _, r := range m.replace {
			got = append(got, fmt.Sprintf("replace %s %s => %s %s", r.old, r.oldVersion, r.new, r.newVersion))
		}
		for _, mod := range mods {
			version := ""
			for _, r := range ref.Require {
				if r.Path == mod {
					version = r.Version
				}
			}
			key := mod + " " + version
			exact, any := "", ""
			for _, r := range ref.Replace {
				target := r.New.Path
				if r.New.Version != "" {
					target += " " + r.New.Version
				}
				switch {
				case r.Old.Path == mod && r.Old.Version == version:
					exact = target
				case r.Old.Path == mod && r.Old.Version == "":
					any = target
				}
			}
			if exact != "" {
				key += " => " + exact
			} else if any != "" {
				key += " => " + any
			}
			want = append(want, "key "+key)
			k, _ := m.moduleKey(mod)
			got = append(got, "key "+k)
		}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("seed %d (replay: ERGO_GOMOD_SEED=%d):\n--- go.mod:\n%s\n--- go mod edit:\n%s\n--- loader:\n%s", seed, seed, text, strings.Join(want, "\n"), strings.Join(got, "\n"))
		}
	}
}

// genGoMod writes a go.mod requiring two to four modules, each maybe
// replaced, in a random mix of forms, and returns it with the module paths
// it requires.
func genGoMod(r *rand.Rand) (string, []string) {
	var b strings.Builder
	word := func(w string) string {
		if r.IntN(4) == 0 {
			return strconv.Quote(w)
		}
		return w
	}
	comment := func() string {
		if r.IntN(3) == 0 {
			return " // indirect"
		}
		return ""
	}
	b.WriteString("// generated\nmodule " + word("example.com/app") + "\n\ngo 1.27\n\n")
	n := 2 + r.IntN(3)
	var mods []string
	var requires, replaces []string
	for i := range n {
		mod := fmt.Sprintf("example.com/m%d", i)
		version := fmt.Sprintf("v1.%d.0", r.IntN(3))
		mods = append(mods, mod)
		requires = append(requires, word(mod)+" "+version+comment())
		switch r.IntN(6) {
		case 1:
			replaces = append(replaces, word(mod)+" => "+word(fmt.Sprintf("../m%d", i)))
		case 2:
			replaces = append(replaces, word(mod)+" "+version+" => "+word(fmt.Sprintf("./local/m%d", i)))
		case 3:
			replaces = append(replaces, mod+" => example.org/fork"+strconv.Itoa(i)+" v0.9.0")
		case 4:
			// one for every version, then one for another version
			replaces = append(replaces, mod+" => ../every"+strconv.Itoa(i), mod+" v1.9.9 => ../never")
		case 5:
			// one for this exact version, then one for every version
			replaces = append(replaces, mod+" "+version+" => ../exact"+strconv.Itoa(i), mod+" => ../every"+strconv.Itoa(i)+"//no space")
		}
	}
	write := func(verb string, lines []string) {
		if len(lines) == 0 {
			return
		}
		if r.IntN(2) == 0 {
			b.WriteString(verb + " (\n")
			for _, l := range lines {
				b.WriteString("\t" + l + "\n")
			}
			b.WriteString(")\n\n")
			return
		}
		for _, l := range lines {
			b.WriteString(verb + " " + l + "\n")
		}
		b.WriteString("\n")
	}
	write("require", requires)
	write("replace", replaces)
	return b.String(), mods
}
