package lint

// Recommended is the base preset: the justifier rules that are on by
// default (plan §3). LINT1 adds foreign-where-std, deprecated,
// unbounded-collect and race-for-timeout; LINT2 adds the strict and layout
// presets.
const Recommended = "recommended"

// Builtin is the registry of the built-in rules and presets.
func Builtin() *Registry {
	r, err := NewRegistry(
		[]*Rule{silentCatch, foreignErrorPublic},
		[]Preset{{
			Name: Recommended,
			Doc:  "The justifier rules: legal code that must say why.",
			Rules: map[string]Setting{
				silentCatch.Name:        {},
				foreignErrorPublic.Name: {},
			},
		}},
		Recommended,
	)
	if err != nil {
		panic(err)
	}
	return r
}
