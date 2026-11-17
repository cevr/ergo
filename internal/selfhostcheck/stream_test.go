package selfhostcheck

import (
	"strconv"
	"testing"

	"ergo.local/prototype/internal/hostsyntax"
)

// TestStreamAgreesWithTree holds the streaming printer (compiler/format
// stream.ergo, capstone S1e) to the tree printer, its reference, over the
// formatter's seeded domain (formatInputs: the repository's sources, the
// corpus, the formatter's rows and fuzz seeds, tapered columns, seeded
// generated grammar inputs, a whitespace perturbation and an edit of each,
// random bytes):
//
//   - on an input the tree printer refuses, the stream gives the same
//     outcome (the same located syntax failure);
//   - on the tree printer's text of an input, the stream gives that text
//     back, byte for byte.
//
// The second is the stream's domain: formatted sources, where `ergo fmt`
// spends its time. On an unformatted source the tree printer still breaks
// lines the source joins (a one-line block of several statements, a union
// whose variant spans lines); the test logs how many raw inputs the two
// print alike. A failing seed prints the input, both outcomes and the
// replay command.
//
// Mutants it alone catches: the st-* rows of
// internal/selfhostcheck/testdata/mutants.json.
func TestStreamAgreesWithTree(t *testing.T) {
	seed := selfhostSeed(t)
	inputs := formatInputs(t, seed)
	replay := "replay: " + seedVariable + "=" + strconv.FormatUint(seed, 10) + " go test -count=1 -run '^TestStreamAgreesWithTree$' ./internal/selfhostcheck"
	failed, formatted, raw := 0, 0, 0
	report := func(name, src, got, want string) {
		failed++
		if failed > 5 {
			return
		}
		line, g, w := firstDiff(got, want)
		t.Errorf("seed %d: input %s: the streaming printer differs from the tree printer at line %d\n  stream: %q\n  tree:   %q\ninput: %q\n%s", seed, name, line, g, w, src, replay)
	}
	for _, in := range inputs {
		text, err := hostsyntax.SourceBounded(in.Src, 0)
		want := outcome(text, err)
		got := outcome(hostsyntax.Stream(in.Src, 0))
		if got == want {
			raw++
		}
		if err != nil {
			if got != want {
				report(in.Name, in.Src, got, want)
			}
			continue
		}
		formatted++
		if back := outcome(hostsyntax.Stream(text, 0)); back != "ok\n"+text {
			report(in.Name+" formatted", text, back, "ok\n"+text)
		}
	}
	t.Logf("seed %d: %d inputs, %d formatted by the tree printer; raw inputs printed alike: %d", seed, len(inputs), formatted, raw)
	if failed > 0 {
		t.Errorf("the printers differ on %d of %d inputs", failed, len(inputs))
	}
}
