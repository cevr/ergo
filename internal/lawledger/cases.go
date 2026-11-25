package lawledger

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// CasesFile is the module-wide regression case store beside ergo.laws:
// shadow refutations (`law <pkg>.<Law> (<values>)`) and property shrunk
// tapes (`property <pkg>.<Name> tape <reach-digest> <draws>`).
const CasesFile = "ergo.cases"

// PropertyCase is one stored shrunk draw tape for a property (a law or a
// parameterized / Draws test), tagged with the property's reach digest at
// the time it was recorded.
type PropertyCase struct {
	Key    string
	Digest string
	Tape   []uint64
}

// CaseStore holds every entry of an ergo.cases file: shadow refutation
// lines (preserving backward compatibility with licence/shadow runs) and
// persistent property tape entries.
type CaseStore struct {
	Shadow   map[string][]string
	Property []PropertyCase
}

// NewCaseStore returns an empty CaseStore.
func NewCaseStore() *CaseStore {
	return &CaseStore{Shadow: map[string][]string{}}
}

// PropertyKey returns the canonical space-free property key stored in
// ergo.cases for a package path and obligation label (e.g. "app.SortLaw",
// "app.TestSort", "app.Store.Put/for/Mem").
func PropertyKey(pkgPath, label string) string {
	name := strings.TrimPrefix(label, "law ")
	name = strings.ReplaceAll(strings.TrimSpace(name), " ", "/")
	if pkgPath == "" {
		return name
	}
	return pkgPath + "." + name
}

// EncodeTape encodes a draw tape as lowercase hex uint64s separated by commas,
// or "-" for an empty tape.
func EncodeTape(tape []uint64) string {
	if len(tape) == 0 {
		return "-"
	}
	parts := make([]string, len(tape))
	for i, v := range tape {
		parts[i] = strconv.FormatUint(v, 16)
	}
	return strings.Join(parts, ",")
}

// DecodeTape decodes a draw tape produced by EncodeTape.
func DecodeTape(s string) ([]uint64, error) {
	if s == "-" {
		return []uint64{}, nil
	}
	if s == "" {
		return nil, fmt.Errorf("empty tape encoding")
	}
	parts := strings.Split(s, ",")
	out := make([]uint64, len(parts))
	for i, p := range parts {
		v, err := strconv.ParseUint(p, 16, 64)
		if err != nil {
			return nil, fmt.Errorf("bad tape draw %q: %w", p, err)
		}
		out[i] = v
	}
	return out, nil
}

// ParseCases parses an ergo.cases file containing shadow refutation lines
// (`law <key> (<values>)`) and/or property tape lines
// (`property <key> tape <digest> <draws>`). Blank lines and `#` comments are
// skipped.
func ParseCases(text string) (*CaseStore, error) {
	out := NewCaseStore()
	for i, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "law "); ok {
			key, c, ok2 := strings.Cut(rest, " ")
			if !ok2 || key == "" || !strings.HasPrefix(c, "(") {
				return nil, fmt.Errorf("%s:%d: not a corpus line: %q", CasesFile, i+1, line)
			}
			out.Shadow[key] = append(out.Shadow[key], c)
			continue
		}
		if rest, ok := strings.CutPrefix(line, "property "); ok {
			fields := strings.Fields(rest)
			if len(fields) != 4 || fields[1] != "tape" || fields[0] == "" || fields[2] == "" {
				return nil, fmt.Errorf("%s:%d: not a property tape line: %q", CasesFile, i+1, line)
			}
			tape, err := DecodeTape(fields[3])
			if err != nil {
				return nil, fmt.Errorf("%s:%d: %w", CasesFile, i+1, err)
			}
			out.Property = append(out.Property, PropertyCase{
				Key:    fields[0],
				Digest: fields[2],
				Tape:   tape,
			})
			continue
		}
		return nil, fmt.Errorf("%s:%d: not a corpus line: %q", CasesFile, i+1, line)
	}
	return out, nil
}

// Format serializes the CaseStore deterministically: shadow lines first sorted
// by key (matching the legacy corpus format byte for byte), followed by
// property tape lines sorted by key while preserving insertion order per key.
func (s *CaseStore) Format() string {
	if s == nil {
		return ""
	}
	var b strings.Builder
	if len(s.Shadow) > 0 {
		keys := make([]string, 0, len(s.Shadow))
		for k := range s.Shadow {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			for _, line := range s.Shadow[k] {
				b.WriteString("law " + k + " " + line + "\n")
			}
		}
	}
	if len(s.Property) > 0 {
		byKey := map[string][]PropertyCase{}
		for _, p := range s.Property {
			byKey[p.Key] = append(byKey[p.Key], p)
		}
		keys := make([]string, 0, len(byKey))
		for k := range byKey {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			for _, p := range byKey[k] {
				b.WriteString("property " + p.Key + " tape " + p.Digest + " " + EncodeTape(p.Tape) + "\n")
			}
		}
	}
	return b.String()
}

// AddProperty appends c if no entry with the same key, digest, and tape
// already exists. It reports whether the store changed.
func (s *CaseStore) AddProperty(c PropertyCase) bool {
	for _, existing := range s.Property {
		if existing.Key == c.Key && existing.Digest == c.Digest && slices.Equal(existing.Tape, c.Tape) {
			return false
		}
	}
	cp := PropertyCase{
		Key:    c.Key,
		Digest: c.Digest,
		Tape:   slices.Clone(c.Tape),
	}
	s.Property = append(s.Property, cp)
	return true
}

// ForProperty returns all stored property cases for key, in stored order.
func (s *CaseStore) ForProperty(key string) []PropertyCase {
	if s == nil {
		return nil
	}
	var out []PropertyCase
	for _, p := range s.Property {
		if p.Key == key {
			out = append(out, PropertyCase{
				Key:    p.Key,
				Digest: p.Digest,
				Tape:   slices.Clone(p.Tape),
			})
		}
	}
	return out
}

// RemoveProperty removes every property entry matching key and match,
// returning how many entries were removed.
func (s *CaseStore) RemoveProperty(key string, match func(PropertyCase) bool) int {
	if s == nil {
		return 0
	}
	kept := s.Property[:0]
	removed := 0
	for _, p := range s.Property {
		if p.Key == key && match(p) {
			removed++
			continue
		}
		kept = append(kept, p)
	}
	s.Property = kept
	return removed
}

// PruneOrphans removes property entries whose key starts with pkgPrefix (e.g.
// "app.") and is not present in liveKeys. Returns the count removed.
func (s *CaseStore) PruneOrphans(pkgPrefix string, liveKeys map[string]bool) int {
	if s == nil {
		return 0
	}
	kept := s.Property[:0]
	removed := 0
	for _, p := range s.Property {
		if strings.HasPrefix(p.Key, pkgPrefix) && !liveKeys[p.Key] {
			removed++
			continue
		}
		kept = append(kept, p)
	}
	s.Property = kept
	return removed
}

// ReadCasesFile reads and parses ergo.cases at path. A missing file returns an
// empty CaseStore and nil error.
func ReadCasesFile(path string) (*CaseStore, error) {
	if path == "" {
		return NewCaseStore(), nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return NewCaseStore(), nil
	}
	if err != nil {
		return nil, err
	}
	return ParseCases(string(data))
}

// WriteCasesFile writes s atomically to path.
func WriteCasesFile(path string, s *CaseStore) error {
	if path == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".ergo.cases-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(s.Format()); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}
