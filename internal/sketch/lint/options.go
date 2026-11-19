package lint

import (
	"encoding/json"
	"fmt"
)

// options is a Rule.Options decoder: the defaults fresh from make, then
// the configured object decoded strictly over them (an unknown field or
// trailing data is refused), then check.
func options[T any](make func() T, check func(T) error) func(json.RawMessage) (any, error) {
	return func(raw json.RawMessage) (any, error) {
		v := make()
		if len(raw) > 0 {
			if err := decodeStrict(raw, &v); err != nil {
				return nil, err
			}
		}
		if check != nil {
			if err := check(v); err != nil {
				return nil, err
			}
		}
		return v, nil
	}
}

// maxOptions is a complexity cap's options: the largest value a function
// may have.
type maxOptions struct {
	Max float64 `json:"max"`
}

func capOptions(max float64) func(json.RawMessage) (any, error) {
	return options(func() maxOptions { return maxOptions{Max: max} }, func(o maxOptions) error {
		if o.Max < 0 {
			return fmt.Errorf("max is %v; it is 0 or more", o.Max)
		}
		return nil
	})
}

// wordOptions is banned-words' lists: words no declared name may hold,
// and generic verbs no top-level function may be named by alone.
// Glossary is a GLOSSARY.md, relative to the configuration document's
// directory, whose _Avoid_ terms no declared name may hold (glossary.go);
// avoid is its terms, read at Configure.
type wordOptions struct {
	Words    []string `json:"words"`
	Verbs    []string `json:"verbs"`
	Glossary string   `json:"glossary"`
	avoid    []avoidTerm
}

// genericVerbs is banned-words' default verbs: a function named by one of
// them alone says it does something, not what (Fowler mysterious-name;
// anti-slop §1c).
var genericVerbs = []string{"handle", "process", "manage", "do", "perform", "execute", "run", "compute"}

// suffixOptions is concept-file-names' list of kind words.
type suffixOptions struct {
	Suffixes []string `json:"suffixes"`
}

// functionOptions is with-at-entry's list of functions, each DIR.Name
// (DIR the package's folder under the module root), where a with may pick
// a program implementation.
type functionOptions struct {
	Functions []string `json:"functions"`
}

// ownerOptions is capability-outside-owner's map from a capability, as
// its type prints (store.Store), to the package folders under the module
// root that may run its operations ("." the root package's).
type ownerOptions struct {
	Owners map[string][]string `json:"owners"`
}
