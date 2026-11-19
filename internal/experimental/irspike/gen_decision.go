package irspike

import (
	"regexp"
	"strings"

	"ergo.local/prototype/internal/sketch/std"
)

// decisionVariant is one variant of the prelude's Decision[S, O], read
// from the prelude's source: its name, its type arguments in Decision's
// parameters (`[S, O]`, or "") and its record's field names.
type decisionVariant struct {
	name, args string
	fields     []string
}

func (v decisionVariant) has(field string) bool {
	for _, f := range v.fields {
		if f == field {
			return true
		}
	}
	return false
}

// at is the variant's type at Decision[s, o]: `Emit[MS, E3]`.
func (v decisionVariant) at(s, o string) string {
	if v.args == "" {
		return v.name
	}
	return v.name + decisionParam.ReplaceAllStringFunc(v.args, func(p string) string {
		if p == "S" {
			return s
		}
		return o
	})
}

// decisionLit is v's literal at Decision[MS, E3]: next for its Next and
// out for its Output.
func decisionLit(v decisionVariant, next, out string) string {
	if len(v.fields) == 0 {
		return v.at("MS", "E3")
	}
	var fs []string
	for _, f := range v.fields {
		switch f {
		case "Next":
			fs = append(fs, "Next: "+next)
		case "Output":
			fs = append(fs, "Output: "+out)
		default:
			panic("irspike: Decision variant " + v.name + " has a field the generator does not draw: " + f)
		}
	}
	return v.at("MS", "E3") + "{" + strings.Join(fs, ", ") + "}"
}

var decisionParam = regexp.MustCompile(`\b[SO]\b`)

// decisionVariants are Decision's variants in the union's order, so the
// generator's literals and its match arms follow the prelude: a variant
// the prelude adds is drawn and matched without a generator change (the
// Tell arm GenerateGeneric once lacked).
func decisionVariants() []decisionVariant {
	src, err := std.Sources.ReadFile("prelude.ergo")
	if err != nil {
		panic("irspike: the prelude: " + err.Error())
	}
	text := string(src)
	_, rest, ok := strings.Cut(text, "\ntype Decision[S, O] = ")
	if !ok {
		panic("irspike: the prelude declares no Decision[S, O]")
	}
	union, _, _ := strings.Cut(rest, "\n")
	var out []decisionVariant
	for _, part := range strings.Split(union, "|") {
		part = strings.TrimSpace(part)
		v := decisionVariant{name: part}
		if i := strings.IndexByte(part, '['); i >= 0 {
			v.name, v.args = part[:i], part[i:]
		}
		_, decl, ok := strings.Cut(text, "\ntype "+v.name)
		if !ok {
			panic("irspike: the prelude declares no " + v.name)
		}
		decl, _, _ = strings.Cut(decl, "\n")
		_, body, _ := strings.Cut(decl, "{")
		body, _, _ = strings.Cut(body, "}")
		for _, f := range strings.Split(body, ",") {
			if f = strings.TrimSpace(f); f != "" {
				v.fields = append(v.fields, strings.Fields(f)[0])
			}
		}
		out = append(out, v)
	}
	return out
}
