package lint

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
)

// Option value types.
const (
	OptionString     = "string"
	OptionBoolean    = "boolean"
	OptionInteger    = "integer"
	OptionStringList = "string-list"
)

const (
	maxOptionSpecs    = 64
	maxOptionStrings  = 256
	maxOptionBytes    = 64 << 10
	maxDescriptionLen = 16 << 10
	maxJSONDepth      = 32
)

// OptionSpec declares one rule option. Enum restricts string and
// string-list values. A required option has no default.
type OptionSpec struct {
	Name        string          `json:"name"`
	Type        string          `json:"type"`
	Description string          `json:"description,omitempty"`
	Required    bool            `json:"required,omitempty"`
	Enum        []string        `json:"enum,omitempty"`
	Default     json.RawMessage `json:"default,omitempty"`
}

// Options are validated option values with defaults applied. Accessors
// return the zero value for an absent optional option without a default.
type Options struct {
	values map[string]any
}

func (o Options) String(name string) string {
	value, _ := o.values[name].(string)
	return value
}

func (o Options) Bool(name string) bool {
	value, _ := o.values[name].(bool)
	return value
}

func (o Options) Int(name string) int64 {
	value, _ := o.values[name].(int64)
	return value
}

func (o Options) StringList(name string) []string {
	value, _ := o.values[name].([]string)
	return slices.Clone(value)
}

// Has reports whether name has a configured or default value.
func (o Options) Has(name string) bool {
	_, ok := o.values[name]
	return ok
}

// canonical renders effective values deterministically for identities.
func (o Options) canonical() json.RawMessage {
	if len(o.values) == 0 {
		return json.RawMessage("{}")
	}
	data, _ := json.Marshal(o.values) // map keys are sorted by encoding/json
	return data
}

func validateOptionSpecs(specs []OptionSpec) error {
	if len(specs) > maxOptionSpecs {
		return fmt.Errorf("declares %d options; the limit is %d", len(specs), maxOptionSpecs)
	}
	seen := map[string]bool{}
	for _, spec := range specs {
		if !validIdentifier(spec.Name) {
			return fmt.Errorf("option name %q must be a lowercase kebab-case identifier", spec.Name)
		}
		if seen[spec.Name] {
			return fmt.Errorf("duplicate option %q", spec.Name)
		}
		seen[spec.Name] = true
		if len(spec.Description) > maxDescriptionLen {
			return fmt.Errorf("option %q description exceeds %d bytes", spec.Name, maxDescriptionLen)
		}
		switch spec.Type {
		case OptionString, OptionStringList:
		case OptionBoolean, OptionInteger:
			if len(spec.Enum) > 0 {
				return fmt.Errorf("option %q: enum applies only to string options", spec.Name)
			}
		default:
			return fmt.Errorf("option %q has unsupported type %q", spec.Name, spec.Type)
		}
		if len(spec.Enum) > maxOptionStrings {
			return fmt.Errorf("option %q enum exceeds %d values", spec.Name, maxOptionStrings)
		}
		if spec.Required && len(spec.Default) > 0 {
			return fmt.Errorf("required option %q cannot have a default", spec.Name)
		}
		if len(spec.Default) > 0 {
			if _, err := decodeOptionValue(spec, spec.Default); err != nil {
				return fmt.Errorf("option %q default: %w", spec.Name, err)
			}
		}
	}
	return nil
}

// ValidateOptions checks raw JSON object options against specs and returns
// effective values. Null or empty raw options mean no configured values.
func ValidateOptions(specs []OptionSpec, raw json.RawMessage) (Options, error) {
	values := map[string]any{}
	configured := map[string]json.RawMessage{}
	if trimmed := bytes.TrimSpace(raw); len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
		if len(trimmed) > maxOptionBytes {
			return Options{}, fmt.Errorf("options exceed %d bytes", maxOptionBytes)
		}
		if err := decodeStrict(trimmed, &configured); err != nil {
			return Options{}, fmt.Errorf("options must be a JSON object: %w", err)
		}
	}
	names := make([]string, 0, len(configured))
	for name := range configured {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		index := slices.IndexFunc(specs, func(spec OptionSpec) bool { return spec.Name == name })
		if index < 0 {
			return Options{}, fmt.Errorf("unknown option %q", name)
		}
		value, err := decodeOptionValue(specs[index], configured[name])
		if err != nil {
			return Options{}, fmt.Errorf("option %q: %w", name, err)
		}
		values[name] = value
	}
	for _, spec := range specs {
		if _, ok := values[spec.Name]; ok {
			continue
		}
		if spec.Required {
			return Options{}, fmt.Errorf("missing required option %q", spec.Name)
		}
		if len(spec.Default) > 0 {
			values[spec.Name], _ = decodeOptionValue(spec, spec.Default)
		}
	}
	return Options{values: values}, nil
}

func decodeOptionValue(spec OptionSpec, raw json.RawMessage) (any, error) {
	enumerated := func(value string) error {
		if len(spec.Enum) > 0 && !slices.Contains(spec.Enum, value) {
			return fmt.Errorf("%q is not one of %s", value, strings.Join(spec.Enum, ", "))
		}
		return nil
	}
	if isNull(raw) {
		return nil, fmt.Errorf("null is not a %s value", spec.Type)
	}
	switch spec.Type {
	case OptionString:
		var value string
		if err := decodeStrict(raw, &value); err != nil {
			return nil, fmt.Errorf("expected a string")
		}
		if err := enumerated(value); err != nil {
			return nil, err
		}
		return value, nil
	case OptionBoolean:
		var value bool
		if err := decodeStrict(raw, &value); err != nil {
			return nil, fmt.Errorf("expected a boolean")
		}
		return value, nil
	case OptionInteger:
		var value int64
		if err := decodeStrict(raw, &value); err != nil {
			return nil, fmt.Errorf("expected an integer")
		}
		return value, nil
	case OptionStringList:
		var items []json.RawMessage
		if err := decodeStrict(raw, &items); err != nil {
			return nil, fmt.Errorf("expected an array of strings")
		}
		if len(items) > maxOptionStrings {
			return nil, fmt.Errorf("exceeds %d values", maxOptionStrings)
		}
		value := make([]string, len(items))
		for i, item := range items {
			if isNull(item) || decodeStrict(item, &value[i]) != nil {
				return nil, fmt.Errorf("expected an array of strings")
			}
			if err := enumerated(value[i]); err != nil {
				return nil, err
			}
		}
		return value, nil
	}
	return nil, fmt.Errorf("unsupported type %q", spec.Type)
}

// isNull reports an explicit JSON null, which encoding/json would otherwise
// decode into a scalar's zero value as if a value had been supplied.
func isNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

// decodeStrict decodes exactly one JSON value, rejecting unknown struct
// fields, duplicate object keys and trailing data. encoding/json alone keeps
// the last duplicate key, which would let one configuration hide another.
func decodeStrict(data []byte, target any) error {
	if err := rejectDuplicateKeys(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("unexpected data after JSON value")
	}
	return nil
}

func rejectDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var value func(depth int) error
	value = func(depth int) error {
		if depth > maxJSONDepth {
			return fmt.Errorf("JSON nesting exceeds %d levels", maxJSONDepth)
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		switch token {
		case json.Delim('{'):
			seen := map[string]bool{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				name := key.(string)
				if seen[name] {
					return fmt.Errorf("duplicate key %q", name)
				}
				seen[name] = true
				if err := value(depth + 1); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case json.Delim('['):
			for decoder.More() {
				if err := value(depth + 1); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		}
		return nil
	}
	return value(0)
}
