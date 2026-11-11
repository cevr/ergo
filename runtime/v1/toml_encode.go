package v1

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// TOMLUnrepresentable is std toml's encode failure: a value TOML has no
// form for (a null, an integer outside int64, text that is not UTF-8, a
// repeated key, a root that is not a table). Key is the dotted path of the
// value, "" for the root.
type TOMLUnrepresentable struct {
	Key, Reason string
}

func (e TOMLUnrepresentable) Error() string {
	return "toml.Unrepresentable{Key:" + e.Key + " Reason:" + e.Reason + "}"
}

// TOMLFromJSON writes the JSON text std json's Encode wrote as a TOML 1.0
// document that TOMLParse reads back to the same JSON value (law
// toml.EncodeRoundTrips): the root object's members that are not tables
// first, as key = value lines, then each table member under its [header],
// its own tables after it. A list is an inline array, and a table inside a
// list an inline table. A string is a basic string with ", \ and the
// control bytes and DEL escaped. A number is written as JSON spelled it:
// an integer when it has no fraction and no exponent, which must fit
// int64, else a float, whose JSON spelling is a TOML float. The float
// strings "NaN" and "Infinity" stay strings, as std json writes them.
//
// It reads the text with the JSON Cursor, which reads the surrogate
// escape \udcXX of a byte of invalid UTF-8 back to the byte, so such text
// fails here: TOML text is UTF-8. It is the inverse path of TOMLParse,
// which writes JSON for the Reader; the encoder of the derived codec
// stays std json's, so TOML adds no second encoder per type.
func TOMLFromJSON(text string) (string, error) {
	c, err := JSONOpen(text, 0, 0)
	if err != nil {
		return "", TOMLUnrepresentable{Reason: "the text is not JSON: " + err.Error()}
	}
	root, err := tomlFromCursor(c, "")
	if err != nil {
		return "", err
	}
	if root.kind != tomlEncTable {
		return "", TOMLUnrepresentable{Reason: "the root is not a table"}
	}
	var b strings.Builder
	if err := root.writeTable(&b, nil); err != nil {
		return "", err
	}
	return b.String(), nil
}

const (
	tomlEncAtom = iota
	tomlEncList
	tomlEncTable
)

// tomlEnc is one value read from the JSON text: an atom's TOML spelling,
// or a list's or a table's parts, in order.
type tomlEnc struct {
	kind  int
	atom  string
	keys  []string
	parts []*tomlEnc
}

func tomlFromCursor(c *Cursor, path string) (*tomlEnc, error) {
	switch c.Kind() {
	case JSONKindNull:
		return nil, TOMLUnrepresentable{Key: path, Reason: "TOML has no null"}
	case JSONKindBool:
		return &tomlEnc{atom: strconv.FormatBool(c.Bool())}, nil
	case JSONKindString:
		s := c.String()
		q, ok := tomlQuote(s)
		if !ok {
			return nil, TOMLUnrepresentable{Key: path, Reason: "the text is not UTF-8"}
		}
		return &tomlEnc{atom: q}, nil
	case JSONKindNumber:
		n := c.Number()
		if !strings.ContainsAny(n, ".eE") {
			if _, err := strconv.ParseInt(n, 10, 64); err != nil {
				return nil, TOMLUnrepresentable{Key: path, Reason: "the integer " + n + " is outside TOML's 64 bits"}
			}
		}
		return &tomlEnc{atom: n}, nil
	case JSONKindList:
		c.BeginList()
		e := &tomlEnc{kind: tomlEncList}
		for i := 0; c.More(); i++ {
			p, err := tomlFromCursor(c, path+"["+strconv.Itoa(i)+"]")
			if err != nil {
				return nil, err
			}
			e.parts = append(e.parts, p)
		}
		c.End()
		return e, nil
	case JSONKindObject:
		c.BeginObject()
		e := &tomlEnc{kind: tomlEncTable}
		for c.More() {
			k := c.Key()
			kp := k
			if path != "" {
				kp = path + "." + k
			}
			for _, have := range e.keys {
				if have == k {
					return nil, TOMLUnrepresentable{Key: kp, Reason: "the key is repeated"}
				}
			}
			if _, ok := tomlQuote(k); !ok {
				return nil, TOMLUnrepresentable{Key: kp, Reason: "the key is not UTF-8"}
			}
			p, err := tomlFromCursor(c, kp)
			if err != nil {
				return nil, err
			}
			e.keys = append(e.keys, k)
			e.parts = append(e.parts, p)
		}
		c.End()
		return e, nil
	}
	return nil, TOMLUnrepresentable{Key: path, Reason: "the text ends early"}
}

// writeTable writes the members of the table e at path: its values, then
// each table under its header.
func (e *tomlEnc) writeTable(b *strings.Builder, path []string) error {
	for i, p := range e.parts {
		if p.kind == tomlEncTable {
			continue
		}
		b.WriteString(tomlKey(e.keys[i]) + " = ")
		p.writeInline(b)
		b.WriteByte('\n')
	}
	for i, p := range e.parts {
		if p.kind != tomlEncTable {
			continue
		}
		sub := append(slicesClip(path), e.keys[i])
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		hs := make([]string, len(sub))
		for j, k := range sub {
			hs[j] = tomlKey(k)
		}
		b.WriteString("[" + strings.Join(hs, ".") + "]\n")
		if err := p.writeTable(b, sub); err != nil {
			return err
		}
	}
	return nil
}

// writeInline writes e as a TOML value on one line.
func (e *tomlEnc) writeInline(b *strings.Builder) {
	switch e.kind {
	case tomlEncAtom:
		b.WriteString(e.atom)
	case tomlEncList:
		b.WriteByte('[')
		for i, p := range e.parts {
			if i > 0 {
				b.WriteString(", ")
			}
			p.writeInline(b)
		}
		b.WriteByte(']')
	case tomlEncTable:
		b.WriteByte('{')
		for i, p := range e.parts {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(" " + tomlKey(e.keys[i]) + " = ")
			p.writeInline(b)
		}
		if len(e.parts) > 0 {
			b.WriteByte(' ')
		}
		b.WriteByte('}')
	}
}

// slicesClip is xs with no room after it, so an append makes a new array.
func slicesClip(xs []string) []string { return xs[:len(xs):len(xs)] }

// tomlKey is k as a bare key when it may be one, else a quoted key.
func tomlKey(k string) string {
	if k != "" && strings.Trim(k, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") == "" {
		return k
	}
	q, _ := tomlQuote(k)
	return q
}

// tomlQuote is s as a TOML basic string, or false when s is not UTF-8.
func tomlQuote(s string) (string, bool) {
	if !utf8.ValidString(s) {
		return "", false
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\r':
			b.WriteString(`\r`)
		case r < 0x20 || r == 0x7f:
			b.WriteString(`\u00`)
			b.WriteByte("0123456789abcdef"[r>>4])
			b.WriteByte("0123456789abcdef"[r&0xf])
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String(), true
}
