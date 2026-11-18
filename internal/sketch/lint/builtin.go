package lint

// The built-in presets. Recommended is the base: the rules on by default,
// each with a published precision at Bar or above (precision.go;
// TestDefaultRulesMeetTheBar). No rule reaches the bar yet, so it is
// empty. Justifiers, Strict and Layout are opt-in (--preset or extends):
// justifiers holds the justifier rules (legal code that must say why;
// they were the base until their read sample, 2026-11-17, put them under
// the bar), strict the complexity caps (q149) and the style rules, layout
// the owner's Go layout (AGENTS.md).
const (
	Recommended = "recommended"
	Justifiers  = "justifiers"
	Strict      = "strict"
	Layout      = "layout"
)

// builtinRules are every built-in rule.
var builtinRules = []*Rule{
	silentCatch, foreignErrorPublic, foreignWhereStd, deprecated, unboundedCollect, raceForTimeout,
	cognitiveComplexity, cyclomaticComplexity, halsteadDifficulty, knownValueWidening, bannedWords,
	withAtEntry, conceptFileNames,
	unusedDeclaration, unusedTypeParameter, duplicateTest, duplicateShape, identicalArms, unbuiltVariant,
}

// builtinPresets are the built-in presets over builtinRules.
func builtinPresets() []Preset {
	on := func(rules ...*Rule) map[string]Setting {
		out := map[string]Setting{}
		for _, r := range rules {
			out[r.Name] = Setting{}
		}
		return out
	}
	return []Preset{
		{
			Name:  Recommended,
			Doc:   "The rules on by default: each has a published precision of at least 0.80.",
			Rules: on(),
		},
		{
			Name:  Justifiers,
			Doc:   "The justifier rules: legal code that must say why.",
			Rules: on(silentCatch, foreignErrorPublic, foreignWhereStd, deprecated, unboundedCollect, raceForTimeout),
		},
		{
			Name: Strict,
			Doc:  "Complexity caps (cognitive and cyclomatic 21, Halstead difficulty 79), style (known-value widening, banned words) and dead or duplicate code: unused declarations and type parameters, duplicate tests and shapes, identical arms, unbuilt variants.",
			Rules: on(cognitiveComplexity, cyclomaticComplexity, halsteadDifficulty, knownValueWidening, bannedWords,
				unusedDeclaration, unusedTypeParameter, duplicateTest, duplicateShape, identicalArms, unbuiltVariant),
		},
		{
			Name:  Layout,
			Doc:   "The Go layout of AGENTS.md: implementations chosen only at the entry, files named by concept.",
			Rules: on(withAtEntry, conceptFileNames),
		},
	}
}

// Builtin is the registry of the built-in rules and presets.
func Builtin() *Registry {
	r, err := NewRegistry(builtinRules, builtinPresets(), Recommended)
	if err != nil {
		panic(err)
	}
	return r
}

// BasePreset is the name and rules of the registry's base preset.
func (r *Registry) BasePreset() Preset { return r.presets[r.base] }
