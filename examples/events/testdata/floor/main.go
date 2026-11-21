// Command floor is the Go-runtime floor of examples/events' `events
// stream` (common.md GO-RUNTIME FLOOR): it tallies a JSON array of events
// read from standard input one at a time, and prints the same report, at
// the least work the Go runtime allows for the job. Against the hand Go
// control (testdata/control: encoding/json's Decoder, Token, More and
// Decode into a struct) it reads the bytes once, in place:
//
//   - one buffer (64 KiB, doubled only for an event longer than half of
//     it), refilled between events: only the event being read is in
//     memory, never the log, and nothing is allocated per event (no token,
//     no string, no reflection);
//   - each event is scanned by a parser of this schema over the buffer and
//     an index held in registers (functions of (b, i) returning i): a key
//     is matched by its bytes, a field's value is checked as the codec
//     checks it (an integer for id, at and bytes, a string for _tag and
//     reason, any JSON value for a key the schema does not name) and kept
//     only when the tally needs it (the tag, the bytes);
//   - an event cut by the buffer's end is scanned again from its start once
//     more input is in: the scan reports "short" (-2) instead of reading on,
//     so the inner loops check one bound, the buffer's;
//   - the tag picks the kind and its required fields once the object ends.
//
// What stays is the job: every byte is read and checked as JSON, every
// event's kind and fields are checked, and the tally is counted.
//
//	floor stream < LOG
package main

import (
	"fmt"
	"io"
	"os"
)

// short is a scan that reached the buffer's end: more input may complete it.
const short = -2

// bad is a scan that found malformed JSON at its index (see why).
const bad = -1

type tally struct{ opened, sent, bytes, closed, failed int }

func space(b []byte, i int) int {
	for i < len(b) && (b[i] == ' ' || b[i] == '\n' || b[i] == '\r' || b[i] == '\t') {
		i++
	}
	return i
}

func hex(c byte) bool { return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F' }

// str scans the string at b[i] (a '"'), and returns the index past it
// and its contents' bounds; escaped is true when it holds an escape.
func str(b []byte, i int) (next, from, to int, escaped bool) {
	if i >= len(b) {
		return short, 0, 0, false
	}
	if b[i] != '"' {
		return bad, 0, 0, false
	}
	i++
	from = i
	for i < len(b) {
		c := b[i]
		switch {
		case c == '"':
			return i + 1, from, i, escaped
		case c < 0x20:
			return bad, 0, 0, false
		case c == '\\':
			escaped = true
			if i+1 >= len(b) {
				return short, 0, 0, false
			}
			switch b[i+1] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				i += 2
			case 'u':
				if i+6 > len(b) {
					return short, 0, 0, false
				}
				if !hex(b[i+2]) || !hex(b[i+3]) || !hex(b[i+4]) || !hex(b[i+5]) {
					return bad, 0, 0, false
				}
				i += 6
			default:
				return bad, 0, 0, false
			}
		default:
			i++
		}
	}
	return short, 0, 0, false
}

// integer scans the JSON number at b[i] that is a Go int.
func integer(b []byte, i int) (next, v int) {
	neg := false
	if i < len(b) && b[i] == '-' {
		neg = true
		i++
	}
	start := i
	for i < len(b) && b[i]-'0' <= 9 {
		d := int(b[i] - '0')
		if v > (1<<63-1-d)/10 {
			return bad, 0
		}
		v = v*10 + d
		i++
	}
	switch {
	case i == len(b):
		return short, 0
	case i == start, i-start > 1 && b[start] == '0', b[i] == '.', b[i] == 'e', b[i] == 'E':
		return bad, 0
	}
	if neg {
		v = -v
	}
	return i, v
}

// value scans any JSON value at b[i].
func value(b []byte, i, depth int) int {
	if depth > 10000 {
		return bad
	}
	if i = space(b, i); i >= len(b) {
		return short
	}
	switch c := b[i]; {
	case c == '"':
		next, _, _, _ := str(b, i)
		return next
	case c == '{' || c == '[':
		end := byte('}')
		if c == '[' {
			end = ']'
		}
		if i = space(b, i+1); i >= len(b) {
			return short
		}
		if b[i] == end {
			return i + 1
		}
		for {
			if c == '{' {
				if i, _, _, _ = str(b, space(b, i)); i < 0 {
					return i
				}
				if i = space(b, i); i >= len(b) {
					return short
				}
				if b[i] != ':' {
					return bad
				}
				i++
			}
			if i = value(b, i, depth+1); i < 0 {
				return i
			}
			if i = space(b, i); i >= len(b) {
				return short
			}
			switch b[i] {
			case ',':
				i++
			case end:
				return i + 1
			default:
				return bad
			}
		}
	case c == '-' || c-'0' <= 9:
		j := i + 1
		for j < len(b) && (b[j]-'0' <= 9 || b[j] == '.' || b[j] == 'e' || b[j] == 'E' || b[j] == '+' || b[j] == '-') {
			j++
		}
		if j == len(b) {
			return short
		}
		if c == '-' && j == i+1 {
			return bad
		}
		return j
	default:
		for _, w := range []string{"true", "false", "null"} {
			if c == w[0] {
				if i+len(w) > len(b) {
					return short
				}
				if string(b[i:i+len(w)]) != w {
					return bad
				}
				return i + len(w)
			}
		}
		return bad
	}
}

// Field bits: which fields an event had.
const (
	hasTag = 1 << iota
	hasID
	hasAt
	hasBytes
	hasReason
)

// event scans the event object at b[i] and counts it into t, or returns
// short or bad (t unchanged), or an error of the schema.
func event(b []byte, i int, t *tally, index int) (int, error) {
	if i = space(b, i); i >= len(b) {
		return short, nil
	}
	if b[i] != '{' {
		return bad, nil
	}
	if i = space(b, i+1); i >= len(b) {
		return short, nil
	}
	tagFrom, tagTo, bytes, seen := 0, 0, 0, 0
	if b[i] != '}' {
		for {
			next, from, to, escaped := str(b, i)
			if next < 0 {
				return next, nil
			}
			if i = space(b, next); i >= len(b) {
				return short, nil
			}
			if b[i] != ':' {
				return bad, nil
			}
			if i = space(b, i+1); i >= len(b) {
				return short, nil
			}
			key := ""
			if !escaped {
				key = string(b[from:to]) // compared only: no allocation
			}
			switch key {
			case "_tag":
				next, from, to, escaped = str(b, i)
				if !escaped {
					tagFrom, tagTo = from, to
				} else {
					tagFrom, tagTo = 0, 0
				}
				seen |= hasTag
			case "id":
				next, _ = integer(b, i)
				seen |= hasID
			case "at":
				next, _ = integer(b, i)
				seen |= hasAt
			case "bytes":
				next, bytes = integer(b, i)
				seen |= hasBytes
			case "reason":
				next, _, _, _ = str(b, i)
				seen |= hasReason
			default:
				next = value(b, i, 0)
			}
			if next < 0 {
				return next, nil
			}
			if i = space(b, next); i >= len(b) {
				return short, nil
			}
			if b[i] == '}' {
				break
			}
			if b[i] != ',' {
				return bad, nil
			}
			i = space(b, i+1)
		}
	}
	i++
	if seen&hasTag == 0 {
		return 0, fmt.Errorf("event [%d]: no _tag", index)
	}
	need := 0
	switch string(b[tagFrom:tagTo]) {
	case "Opened":
		need = hasID | hasAt
	case "Sent":
		need = hasID | hasBytes
	case "Closed":
		need = hasID
	case "Failed":
		need = hasID | hasReason
	default:
		return 0, fmt.Errorf("event [%d]: no event kind %s", index, string(b[tagFrom:tagTo]))
	}
	if seen&need != need {
		return 0, fmt.Errorf("event [%d]: a field is missing", index)
	}
	switch need {
	case hasID | hasAt:
		t.opened++
	case hasID | hasBytes:
		t.sent++
		t.bytes += bytes
	case hasID:
		t.closed++
	default:
		t.failed++
	}
	return i, nil
}

// input is standard input in a buffer: b[i:] is what is read and not yet
// scanned; off is the input offset of b[0].
type input struct {
	in  io.Reader
	buf []byte
	b   []byte
	i   int
	off int
	eof bool
}

// more keeps b[i:] and reads more after it; it reports false at the end
// of the input.
func (r *input) more() (bool, error) {
	if r.eof {
		return false, nil
	}
	rest := len(r.b) - r.i
	if rest > len(r.buf)/2 {
		r.buf = make([]byte, 2*len(r.buf))
	}
	r.off += r.i
	n := copy(r.buf, r.b[r.i:])
	for {
		m, err := r.in.Read(r.buf[n:])
		n += m
		if err == io.EOF {
			r.eof = true
			break
		}
		if err != nil {
			return false, err
		}
		if m > 0 {
			break
		}
	}
	r.b, r.i = r.buf[:n], 0
	return n > rest, nil
}

func (r *input) fail(at int) error {
	return fmt.Errorf("not JSON at byte %d", r.off+at)
}

// expect scans space and then c, reading more input as it needs.
func (r *input) expect(c byte) error {
	for {
		i := space(r.b, r.i)
		if i < len(r.b) {
			if r.b[i] != c {
				return r.fail(i)
			}
			r.i = i + 1
			return nil
		}
		r.i = i
		if ok, err := r.more(); err != nil || !ok {
			if err == nil {
				err = r.fail(r.i)
			}
			return err
		}
	}
}

// peek is the next byte after space, reading more as it needs; 0 at the end.
func (r *input) peek() (byte, error) {
	for {
		r.i = space(r.b, r.i)
		if r.i < len(r.b) {
			return r.b[r.i], nil
		}
		if ok, err := r.more(); err != nil || !ok {
			return 0, err
		}
	}
}

func run(in io.Reader) (tally, error) {
	var t tally
	r := &input{in: in, buf: make([]byte, 65536)}
	if err := r.expect('['); err != nil {
		return t, err
	}
	c, err := r.peek()
	if err != nil {
		return t, err
	}
	if c == ']' {
		r.i++
	} else {
		for index := 0; ; index++ {
			for {
				i, err := event(r.b, r.i, &t, index)
				if err != nil {
					return t, err
				}
				if i >= 0 {
					r.i = i
					break
				}
				if i == bad {
					return t, r.fail(r.i)
				}
				if ok, err := r.more(); err != nil || !ok {
					if err == nil {
						err = r.fail(len(r.b))
					}
					return t, err
				}
			}
			c, err := r.peek()
			if err != nil {
				return t, err
			}
			r.i++
			if c == ']' {
				break
			}
			if c != ',' {
				return t, r.fail(r.i - 1)
			}
		}
	}
	if c, err := r.peek(); err != nil || c != 0 {
		if err == nil {
			err = r.fail(r.i)
		}
		return t, err
	}
	return t, nil
}

func main() {
	if len(os.Args) != 2 || os.Args[1] != "stream" {
		fmt.Fprintln(os.Stderr, "usage: floor stream < LOG")
		os.Exit(2)
	}
	t, err := run(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "events:", err)
		os.Exit(1)
	}
	fmt.Printf("opened %d\nsent %d (%d bytes)\nclosed %d\nfailed %d\nopen %d\n",
		t.opened, t.sent, t.bytes, t.closed, t.failed, t.opened-t.closed-t.failed)
}
