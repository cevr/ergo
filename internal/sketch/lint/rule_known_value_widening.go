package lint

import "ergo.local/prototype/internal/sketch/check"

// knownValueWidening is the strict rule for a let that writes a union
// type over a value whose variant is known (effect-oxlint
// noKnownValueWidening): the written type drops what the value states, so
// later code must match to get it back. The fact is the checker's implicit
// variant conversion at the value (check.ConvVariant). A let mut keeps the
// wider type, since a later assignment may store another variant.
var knownValueWidening = &Rule{
	Name:    "known-value-widening",
	Code:    "L110",
	Doc:     "A let whose written union type widens a value of one known variant drops what the value states; write no type, or the variant's.",
	Instead: "write no type, so the binding keeps the variant",
	Default: Warning,
	Check:   checkKnownValueWidening,
}

func checkKnownValueWidening(p *Pass) {
	for _, d := range p.fileIndex().declStmts {
		ok := d != nil
		if !ok || d.Decl.Mut.IsValid() {
			continue
		}
		for _, s := range d.Decl.Specs {
			if s.Type == nil || len(s.Values) != 1 || len(s.Names) != 1 {
				continue
			}
			if conv, ok := p.Info.Implicit[s.Values[0]]; ok && conv.Kind == check.ConvVariant {
				p.Report(s.Type.Pos(), s.Type.End(), "this type widens a value whose variant is known")
			}
		}
	}
}
