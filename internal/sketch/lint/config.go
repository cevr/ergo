package lint

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ConfigVersion is the version of the configuration document.
const ConfigVersion = 1

// ConfigFile is the configuration document's name: it sits beside the
// module's ergo.mod and is read when present.
const ConfigFile = "ergo.lint.json"

// MaxConfigBytes bounds one configuration document.
const MaxConfigBytes = 1 << 20

// Config is a configuration document (docs/specs/custom-lint.md, version
// 1): presets to extend, in order, and rule settings that apply last.
// Rule packs (packs) return with LINT3.
type Config struct {
	Version int
	Extends []string
	Rules   map[string]Setting
}

// DefaultConfig is the configuration of a module with no document: the
// base preset alone.
func DefaultConfig() Config { return Config{Version: ConfigVersion} }

// Setting is one rule's setting. HasSeverity is false when the setting
// states options only.
type Setting struct {
	Severity    Severity
	HasSeverity bool
	Options     json.RawMessage
}

// The runner's reserved names: no rule, preset entry or directive may name
// them.
var runnerNames = [...]string{"lint-runner", "invalid-suppression"}

// The reserved namespaces of qualified names.
var reservedNamespaces = [...]string{"ergo", "builtin"}

func reservedName(name string) bool {
	for _, r := range runnerNames {
		if r == name {
			return true
		}
	}
	return false
}

// parseSeverity reads a severity word of the configuration.
func parseSeverity(word string) (Severity, error) {
	for s, name := range severityNames {
		if name == word {
			return Severity(s), nil
		}
	}
	return Off, fmt.Errorf("invalid severity %q; use off, error, warning, information or hint", word)
}

// UnmarshalJSON reads a bare severity string or {"severity", "options"}.
func (s *Setting) UnmarshalJSON(data []byte) error {
	var word string
	if err := json.Unmarshal(data, &word); err == nil {
		severity, err := parseSeverity(word)
		if err != nil {
			return err
		}
		*s = Setting{Severity: severity, HasSeverity: true}
		return nil
	}
	var object struct {
		Severity json.RawMessage `json:"severity"`
		Options  map[string]any  `json:"options"`
	}
	if err := decodeStrict(data, &object); err != nil {
		return fmt.Errorf("a rule setting is a severity or {\"severity\", \"options\"}: %w", err)
	}
	fresh := Setting{}
	if object.Severity != nil {
		// A null leaves word empty, which no severity is.
		word = ""
		if err := json.Unmarshal(object.Severity, &word); err != nil {
			return fmt.Errorf("a rule severity is a string; omit it to keep the default")
		}
		severity, err := parseSeverity(word)
		if err != nil {
			return err
		}
		fresh.Severity, fresh.HasSeverity = severity, true
	}
	if len(object.Options) > 0 {
		fresh.Options, _ = json.Marshal(object.Options)
	}
	*s = fresh
	return nil
}

// ParseConfig strictly decodes a configuration document: unknown fields,
// duplicate keys, a null where a value is due and trailing data are
// refused.
func ParseConfig(data []byte) (Config, error) {
	if len(data) > MaxConfigBytes {
		return Config{}, fmt.Errorf("%s exceeds %d bytes", ConfigFile, MaxConfigBytes)
	}
	if err := noDuplicateKeys(data); err != nil {
		return Config{}, fmt.Errorf("invalid %s: %w", ConfigFile, err)
	}
	var doc struct {
		Version *int               `json:"version"`
		Packs   json.RawMessage    `json:"packs"`
		Extends []string           `json:"extends"`
		Rules   map[string]Setting `json:"rules"`
	}
	if err := decodeStrict(data, &doc); err != nil {
		return Config{}, fmt.Errorf("invalid %s: %w", ConfigFile, err)
	}
	if doc.Version == nil {
		return Config{}, fmt.Errorf("invalid %s: version is required", ConfigFile)
	}
	if doc.Packs != nil {
		return Config{}, fmt.Errorf("invalid %s: rule packs return with LINT3; remove packs", ConfigFile)
	}
	return Config{Version: *doc.Version, Extends: doc.Extends, Rules: doc.Rules}, nil
}

// decodeStrict decodes one JSON value with no unknown field and nothing
// after it.
func decodeStrict(data []byte, v any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("data after the JSON value")
	}
	return nil
}

// noDuplicateKeys refuses an object that states a key twice, at any depth.
func noDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			keys := map[string]bool{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				name, _ := key.(string)
				if keys[name] {
					return fmt.Errorf("key %q is stated twice", name)
				}
				keys[name] = true
				if err := walk(); err != nil {
					return err
				}
			}
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
		}
		_, err = decoder.Token() // the closing delimiter
		return err
	}
	return walk()
}
