package rttrace

import (
	"strconv"
	"unicode/utf8"
)

// The writer encodes its few fixed shapes by hand instead of importing
// encoding/json: every emitted kernel program links this package for its
// trace switch, and measured, encoding/json added about 1.5 MB to each
// example binary (docs/research/traceability.md, Cost when off). The field
// names match the json tags on Header, Line, Reason and Frame, which readers
// decode with encoding/json.

func (h Header) appendJSON(b []byte) []byte {
	b = append(b, `{"schema":`...)
	b = appendString(b, h.Schema)
	b = append(b, `,"revision":`...)
	b = appendString(b, h.Revision)
	b = append(b, `,"producer":`...)
	b = appendString(b, h.Producer)
	b = append(b, `,"capacity":`...)
	b = strconv.AppendInt(b, int64(h.Capacity), 10)
	b = append(b, `,"events":`...)
	b = strconv.AppendInt(b, int64(h.Events), 10)
	b = append(b, `,"dropped":`...)
	b = strconv.AppendUint(b, h.Dropped, 10)
	if h.InFlight != 0 {
		b = append(b, `,"inFlight":`...)
		b = strconv.AppendInt(b, int64(h.InFlight), 10)
	}
	return append(b, "}\n"...)
}

func (l Line) appendJSON(b []byte) []byte {
	b = append(b, `{"seq":`...)
	b = strconv.AppendUint(b, l.Seq, 10)
	b = append(b, `,"time":`...)
	b = strconv.AppendInt(b, l.Time, 10)
	b = append(b, `,"id":`...)
	b = strconv.AppendUint(b, l.ID, 10)
	if l.Link != 0 {
		b = append(b, `,"link":`...)
		b = strconv.AppendUint(b, l.Link, 10)
	}
	b = append(b, `,"kind":`...)
	b = appendString(b, l.Kind)
	b = append(b, `,"task":`...)
	b = strconv.AppendUint(b, l.Task, 10)
	b = append(b, `,"parent":`...)
	b = strconv.AppendUint(b, l.Parent, 10)
	b = append(b, `,"def":`...)
	b = appendString(b, l.Def)
	if l.Site != "" {
		b = append(b, `,"site":`...)
		b = appendString(b, l.Site)
	}
	if len(l.Cause) > 0 {
		b = append(b, `,"cause":[`...)
		for i, r := range l.Cause {
			if i > 0 {
				b = append(b, ',')
			}
			b = r.appendJSON(b)
		}
		b = append(b, ']')
	}
	if l.Machine != "" {
		b = append(b, `,"machine":`...)
		b = appendString(b, l.Machine)
	}
	return append(b, "}\n"...)
}

func (r Reason) appendJSON(b []byte) []byte {
	b = append(b, `{"reason":`...)
	b = appendString(b, r.Reason)
	if r.Name != "" {
		b = append(b, `,"name":`...)
		b = appendString(b, r.Name)
	}
	if r.InRelease {
		b = append(b, `,"inRelease":true`...)
	}
	if len(r.Chain) > 0 {
		b = append(b, `,"chain":[`...)
		for i, f := range r.Chain {
			if i > 0 {
				b = append(b, ',')
			}
			b = append(b, `{"task":`...)
			b = strconv.AppendUint(b, f.Task, 10)
			b = append(b, `,"def":`...)
			b = appendString(b, f.Def)
			b = append(b, '}')
		}
		b = append(b, ']')
	}
	if r.Origin != 0 {
		b = append(b, `,"origin":`...)
		b = strconv.AppendUint(b, r.Origin, 10)
	}
	return append(b, '}')
}

const hexDigits = "0123456789abcdef"

// appendString appends s as a JSON string. It escapes the quote, the
// backslash and control characters, and replaces invalid UTF-8 with U+FFFD,
// as encoding/json does.
func appendString(b []byte, s string) []byte {
	b = append(b, '"')
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			switch {
			case c == '"' || c == '\\':
				b = append(b, '\\', c)
			case c < 0x20:
				b = append(b, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xf])
			default:
				b = append(b, c)
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b = append(b, `�`...)
		} else {
			b = append(b, s[i:i+size]...)
		}
		i += size
	}
	return append(b, '"')
}
