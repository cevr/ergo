package lower

import (
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestCellEvidenceFollowsTheDeclaringModule: a cell provider's laws
// rewrite its events on the evidence of the ledger of the module that
// declares it, wherever in the build the provider lives (owner,
// 2026-11-12: the standard library gets no special treatment; STD-NOPRIV
// NP2). The generated Tape writer of TestCellWriterAgrees (tapeProgram),
// whose laws hold by construction, is declared (a) in the root package,
// (b) in a package of the root's module that the root imports, and (c) in
// a package the compiler ships (check.StdFixture), with a shipped ledger
// that holds its proved entries. The root module's ledger holds proved
// entries for the root module's providers alone. The caller's lowered
// encode is the same text in the three, and it is rewritten: the
// reference lowering's text differs. That the rewritten text prints what
// the reference prints is TestCellWriterAgrees, on the same generator.
//
// Mutants it alone catches: copy-evidence-std-only (cellRewriter takes
// evidence for the root package and std only, so (b) is not rewritten)
// and evidence-from-wrong-module (the root's ledger answers for a provider
// another module declares, so (c) is refused). ERGO_CELL_EVIDENCE_SEED
// replays a seed.
func TestCellEvidenceFollowsTheDeclaringModule(t *testing.T) {
	seeds := []int64{1, 2, 3}
	if s := os.Getenv("ERGO_CELL_EVIDENCE_SEED"); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = []int64{v}
	}
	for _, seed := range seeds {
		cellEvidenceSeed(t, seed)
	}
}

// cellEvidencePlace is where the seed's Tape is declared.
type cellEvidencePlace struct {
	name string
	pkg  string // the provider's package path; "" for the root
	ship bool   // a package the compiler ships
}

func cellEvidenceSeed(t *testing.T, seed int64) {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	provider := tapeSource(rng)
	records, kinds := tapeRecords(rng)
	replay := fmt.Sprintf("replay: ERGO_CELL_EVIDENCE_SEED=%d go test -run 'TestCellEvidenceFollowsTheDeclaringModule$' ./internal/sketch/lower", seed)
	places := []cellEvidencePlace{{name: "root"}, {name: "module", pkg: "tapes"}, {name: "shipped", pkg: "tapeship", ship: true}}
	var texts []string
	for _, place := range places {
		caller := cellEvidenceCaller(seed, place, records, kinds)
		text := cellEvidenceLower(t, place, provider, caller, replay, false)
		texts = append(texts, text)
		if place.name == "root" {
			if ref := cellEvidenceLower(t, place, provider, caller, replay, true); ref == text {
				t.Fatalf("seed %d: the root's encode is not rewritten: the property would hold with no evidence at all\n%s", seed, replay)
			}
		}
	}
	for i := 1; i < len(places); i++ {
		if texts[i] != texts[0] {
			x, y := firstDiffLine(texts[0], texts[i])
			t.Fatalf("seed %d: the encode of a Tape declared in %s lowers differently from one declared in the root package\nroot: %s\n%s: %s\nprovider:\n%s\n%s", seed, places[i].name, x, places[i].name, y, provider, replay)
		}
	}
}

// cellEvidenceCaller is the caller of seed: the records and a main that
// prints the text of drawn values, one Tape each, from the package the
// place names. The values are drawn once per seed, so the three callers
// differ only in how they name the Tape's package.
func cellEvidenceCaller(seed int64, place cellEvidencePlace, records string, kinds []tapeField) string {
	rng := rand.New(rand.NewSource(seed * 7919))
	qual := ""
	var b strings.Builder
	b.WriteString("import json\n")
	if place.pkg != "" {
		qual = place.pkg + "."
		b.WriteString("import " + place.pkg + "\n")
	}
	b.WriteString("\n" + records + "\nfunc main() needs Console {\n")
	for i := range 12 {
		k := kinds[i%len(kinds)]
		fmt.Fprintf(&b, "\tlet t%d = %sNewTape()\n\tencode(%s, t%d)\n\tConsole.Println(\"%d \\(t%d.Written())\")\n", i, qual, k.value(rng, 0), i, i, i)
	}
	b.WriteString("}\n")
	return b.String()
}

// cellEvidenceLower lowers the caller with the Tape declared at place,
// on the root module's ledger and the shipped one (the reference
// lowering when reference is set), and returns the caller's encode.
func cellEvidenceLower(t *testing.T, place cellEvidencePlace, provider, caller, replay string, reference bool) string {
	t.Helper()
	// The Tape itself uses no json; the caller's records do.
	provider = strings.Replace(provider, "import json\n", "", 1)
	src := caller
	if place.pkg == "" {
		src = "import json\n" + provider + "\n" + strings.Replace(caller, "import json\n", "", 1)
	}
	imp := pkgImporter{}
	var imports []Unit
	var provPkg *check.Package
	switch {
	case place.ship:
		f, err := syntax.Parse(provider)
		if err != nil {
			t.Fatalf("%v\n%s", err, replay)
		}
		pkg, ds := check.StdFixture(place.pkg, []*syntax.File{f})
		if len(ds) > 0 {
			t.Fatalf("the shipped Tape: %v\n%s", ds, replay)
		}
		imp[place.pkg], provPkg = pkg, pkg
	case place.pkg != "":
		u := checkPackage(t, place.pkg, []pkgSource{{"/ergo-test/" + place.pkg + "/tape.ergo", provider}}, nil)
		imp[place.pkg], provPkg = u.Package, u.Package
		imports = append(imports, u)
	}
	root := checkPackage(t, "main", []pkgSource{{testSourcePath, src}}, imp)
	if provPkg == nil {
		provPkg = root.Package
	}
	root.Imports = imports
	// The shipped ledger holds the proved entries of every law of the
	// shipped Tape, at no instance, as std's does.
	shipped := map[string]LedgerEntry{}
	if place.ship {
		for prov, rules := range newRewriter(provPkg, nil).rules {
			for _, r := range rules {
				e := LedgerEntry{Law: LedgerNames(nil).law(r.law), Provider: LedgerNames(nil).provider(prov), Verdict: "proved", Digest: LawDigest(r.law, prov, r.conv, r.ctor, "")}
				shipped[e.Key()] = e
			}
		}
	}
	saved, savedRewrite := shippedLedger, rewriteLowering
	defer func() { shippedLedger, rewriteLowering = saved, savedRewrite }()
	shippedLedger = func() string { return FormatLedger(shipped) }
	// The root module's ledger: a proved entry for every site the build
	// asks about of a provider the root module declares, at its
	// constants, as ergo test writes them.
	asked := &askedEvidence{}
	root.Rewrite = asked
	if _, err := Lower(root); err != nil {
		t.Fatalf("the asking lowering: %v\n%s", err, replay)
	}
	entries := map[string]LedgerEntry{}
	for _, a := range asked.sites {
		if a.prov.Obj.Pkg != root.Package && (place.ship || a.prov.Obj.Pkg != provPkg) {
			continue
		}
		var r *rwRule
		for _, rule := range newRewriter(a.prov.Obj.Pkg, nil).rules[a.prov] {
			if rule.law == a.law {
				r = &rule
			}
		}
		if r == nil {
			t.Fatalf("no rule of %s for %s\n%s", a.law.Name, a.prov, replay)
		}
		inst := instanceText(a.in)
		e := LedgerEntry{Law: LedgerNames(nil).law(a.law), Provider: LedgerNames(nil).provider(a.prov), Instance: inst, Verdict: "proved", Digest: LawDigest(a.law, a.prov, r.conv, r.ctor, inst)}
		entries[e.Key()] = e
	}
	root.Rewrite = NewLedgerEvidence(root.Package, nil, entries)
	if reference {
		rewriteLowering = "reference"
	}
	files, err := Lower(root)
	if err != nil {
		t.Fatalf("the %s lowering: %v\n%s", place.name, err, replay)
	}
	var b strings.Builder
	for _, f := range files {
		b.Write(f.Data)
	}
	enc := cellEvidenceEncode(b.String())
	if enc == "" {
		t.Fatalf("the %s lowering writes no codec function\n%s", place.name, replay)
	}
	return enc
}

// cellEvidenceEncode is the caller's encode in the Go text: the codec
// functions (ergoWrite…) the lowering writes for its records, each from
// its func line to its closing brace. They are the caller's; where the
// Tape is declared changes how its own declarations ship, not them.
func cellEvidenceEncode(text string) string {
	var b strings.Builder
	in := false
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "func ergoWrite") {
			in = true
		}
		if in {
			b.WriteString(line + "\n")
		}
		if line == "}" {
			in = false
		}
	}
	return b.String()
}

// askedEvidence admits every law and records each site it was asked
// about: the sites a ledger needs entries for.
type askedEvidence struct {
	sites []askedSite
}

type askedSite struct {
	law  *check.Law
	prov *check.Named
	in   RewriteInstance
}

func (e *askedEvidence) Admit(law *check.Law, prov *check.Named, in RewriteInstance) (bool, string) {
	e.sites = append(e.sites, askedSite{law, prov, in})
	return true, ""
}

// firstDiffLine is the first line at which a and b differ.
func firstDiffLine(a, b string) (string, string) {
	x, y := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := range min(len(x), len(y)) {
		if x[i] != y[i] {
			return x[i], y[i]
		}
	}
	if len(x) > len(y) {
		return x[len(y)], ""
	}
	if len(y) > len(x) {
		return "", y[len(x)]
	}
	return "", ""
}
