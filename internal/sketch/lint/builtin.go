package lint

// The built-in presets. Recommended is the base: the justifier rules that
// are on by default (plan §3). Strict and Layout are opt-in (--preset or
// extends): strict holds the complexity caps (q149) and the style rules,
// layout the owner's Go layout (AGENTS.md).
const (
	Recommended = "recommended"
	Strict      = "strict"
	Layout      = "layout"
)

// builtinRules are every built-in rule.
var builtinRules = []*Rule{
	silentCatch, foreignErrorPublic, foreignWhereStd, deprecated, unboundedCollect, raceForTimeout,
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
			Doc:   "The justifier rules: legal code that must say why.",
			Rules: on(silentCatch, foreignErrorPublic, foreignWhereStd, deprecated, unboundedCollect, raceForTimeout),
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
