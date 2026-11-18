// Command floor is the Go-runtime floor of examples/events' `events
// stream` (common.md GO-RUNTIME FLOOR): it tallies a JSON array of events
// read from standard input one at a time, and prints the same report, at
// the least work the Go runtime allows for the job. Against the hand Go
// control (testdata/control: encoding/json's Decoder, Token, More and
// Decode into a struct) it reads the bytes once, in place:
//
//   - one 64 KiB buffer, refilled as the scan reaches its end: only the
//     byte being read is in memory, never the log, and nothing is
//     allocated after the start (no token, no string, no reflection);
//   - each event is scanned by a parser of this schema: a key is matched by
//     its bytes, a field's value is checked as the codec checks it (an
//     integer for id, at and bytes, a string for _tag and reason, any JSON
//     value for a key the schema does not name) and kept only when the
//     tally needs it (the tag, the bytes);
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

type reader struct {
	in   io.Reader
	buf  []byte
	i, n int
	off  int // bytes before buf[0]
	eof  bool
	err  error
}

// fill reads more input once buf is used up; it reports whether a byte is there.
//
//go:noinline
func (r *reader) fill() bool {
	if r.eof {
		return false
	}
	r.off += r.n
	r.i, r.n = 0, 0
	for r.n == 0 {
		n, err := r.in.Read(r.buf)
		r.n = n
		if err == io.EOF {
			r.eof = true
			return n > 0
		}
		if err != nil {
			r.err, r.eof = err, true
			return n > 0
		}
	}
	return true
}

// peek is the next byte, or 0 at the end of the input.
func (r *reader) peek() byte {
	if r.i < r.n || r.fill() {
		return r.buf[r.i]
	}
	return 0
}

func (r *reader) skipSpace() byte {
	for {
		c := r.peek()
		if c != ' ' && c != '\n' && c != '\r' && c != '\t' {
			return c
		}
		r.i++
	}
}

func (r *reader) fail(why string) error {
	return fmt.Errorf("not JSON at byte %d: %s", r.off+r.i, why)
}

func (r *reader) expect(c byte) error {
	if r.skipSpace() != c {
		return r.fail(fmt.Sprintf("want %q", c))
	}
	r.i++
	return nil
}

// str scans a string at the reader; with into, it keeps up to len(into)
// bytes of it in into and reports how many (more than len(into) when it
// is longer). The scan checks every escape and refuses control bytes.
func (r *reader) str(into []byte) (int, error) {
	if r.skipSpace() != '"' {
		return 0, r.fail("want a string")
	}
	r.i++
	n := 0
	for {
		if r.i >= r.n && !r.fill() {
			return 0, r.fail("unterminated string")
		}
		c := r.buf[r.i]
		r.i++
		switch {
		case c == '"':
			return n, nil
		case c < 0x20:
			return 0, r.fail("control byte in a string")
		case c == '\\':
			e := r.peek()
			r.i++
			switch e {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
			case 'u':
				for k := 0; k < 4; k++ {
					h := r.peek()
					r.i++
					if !('0' <= h && h <= '9' || 'a' <= h && h <= 'f' || 'A' <= h && h <= 'F') {
						return 0, r.fail("bad \\u escape")
					}
				}
			default:
				return 0, r.fail("bad escape")
			}
			n = len(into) + 1 // an escaped key or tag matches no name of the schema
		default:
			if n < len(into) {
				into[n] = c
			}
			n++
		}
	}
}

// integer scans a JSON number that is a Go int.
func (r *reader) integer() (int, error) {
	neg := false
	if r.skipSpace() == '-' {
		neg = true
		r.i++
	}
	c := r.peek()
	if c < '0' || c > '9' {
		return 0, r.fail("want an integer")
	}
	v, digits, first := 0, 0, c
	for c >= '0' && c <= '9' {
		if v > (1<<63-1-int(c-'0'))/10 {
			return 0, r.fail("integer out of range")
		}
		v = v*10 + int(c-'0')
		digits++
		r.i++
		c = r.peek()
	}
	if digits > 1 && first == '0' {
		return 0, r.fail("leading zero")
	}
	if c == '.' || c == 'e' || c == 'E' {
		return 0, r.fail("want an integer")
	}
	if neg {
		v = -v
	}
	return v, nil
}

// skipValue scans any JSON value.
func (r *reader) skipValue(depth int) error {
	if depth > 10000 {
		return r.fail("too deep")
	}
	switch c := r.skipSpace(); {
	case c == '"':
		_, err := r.str(nil)
		return err
	case c == '{':
		r.i++
		if r.skipSpace() == '}' {
			r.i++
			return nil
		}
		for {
			if _, err := r.str(nil); err != nil {
				return err
			}
			if err := r.expect(':'); err != nil {
				return err
			}
			if err := r.skipValue(depth + 1); err != nil {
				return err
			}
			switch r.skipSpace() {
			case ',':
				r.i++
			case '}':
				r.i++
				return nil
			default:
				return r.fail("want , or }")
			}
		}
	case c == '[':
		r.i++
		if r.skipSpace() == ']' {
			r.i++
			return nil
		}
		for {
			if err := r.skipValue(depth + 1); err != nil {
				return err
			}
			switch r.skipSpace() {
			case ',':
				r.i++
			case ']':
				r.i++
				return nil
			default:
				return r.fail("want , or ]")
			}
		}
	case c == '-' || '0' <= c && c <= '9':
		if c == '-' {
			r.i++
		}
		digits := 0
		for c = r.peek(); '0' <= c && c <= '9' || c == '.' || c == 'e' || c == 'E' || c == '+' || c == '-'; c = r.peek() {
			digits++
			r.i++
		}
		if digits == 0 {
			return r.fail("bad number")
		}
		return nil
	default:
		for _, w := range []string{"true", "false", "null"} {
			if c == w[0] {
				for k := 0; k < len(w); k++ {
					if r.peek() != w[k] {
						return r.fail("bad literal")
					}
					r.i++
				}
				return nil
			}
		}
		return r.fail("want a value")
	}
}

type tally struct{ opened, sent, bytes, closed, failed int }

// Field bits: which fields an event had.
const (
	hasTag = 1 << iota
	hasID
	hasAt
	hasBytes
	hasReason
)

// event scans one event object and counts it.
func (r *reader) event(t *tally, index int) error {
	if err := r.expect('{'); err != nil {
		return err
	}
	var key, tag [8]byte
	tagLen, bytes, seen := 0, 0, 0
	if r.skipSpace() == '}' {
		r.i++
		return fmt.Errorf("event [%d]: no _tag", index)
	}
	for {
		n, err := r.str(key[:])
		if err != nil {
			return err
		}
		if err := r.expect(':'); err != nil {
			return err
		}
		if n > len(key) {
			n = 0 // longer than every name of the schema: matches none
		}
		switch string(key[:n]) { // compared only: no allocation
		case "":
			if err = r.skipValue(0); err != nil {
				return err
			}
		case "_tag":
			if tagLen, err = r.str(tag[:]); err != nil {
				return err
			}
			seen |= hasTag
		case "id":
			if _, err = r.integer(); err != nil {
				return err
			}
			seen |= hasID
		case "at":
			if _, err = r.integer(); err != nil {
				return err
			}
			seen |= hasAt
		case "bytes":
			if bytes, err = r.integer(); err != nil {
				return err
			}
			seen |= hasBytes
		case "reason":
			if _, err = r.str(nil); err != nil {
				return err
			}
			seen |= hasReason
		default:
			if err = r.skipValue(0); err != nil {
				return err
			}
		}
		c := r.skipSpace()
		r.i++
		if c == '}' {
			break
		}
		if c != ',' {
			r.i--
			return r.fail("want , or }")
		}
	}
	if seen&hasTag == 0 {
		return fmt.Errorf("event [%d]: no _tag", index)
	}
	need := 0
	switch string(tag[:min(tagLen, len(tag))]) {
	case "Opened":
		need = hasID | hasAt
		t.opened++
	case "Sent":
		need = hasID | hasBytes
		t.sent++
		t.bytes += bytes
	case "Closed":
		need = hasID
		t.closed++
	case "Failed":
		need = hasID | hasReason
		t.failed++
	default:
		return fmt.Errorf("event [%d]: no event kind %s", index, tag[:min(tagLen, len(tag))])
	}
	if seen&need != need {
		return fmt.Errorf("event [%d]: a field is missing", index)
	}
	return nil
}

func run(in io.Reader) (tally, error) {
	var t tally
	r := &reader{in: in, buf: make([]byte, 65536)}
	if err := r.expect('['); err != nil {
		return t, err
	}
	if r.skipSpace() == ']' {
		r.i++
	} else {
		for index := 0; ; index++ {
			if err := r.event(&t, index); err != nil {
				return t, err
			}
			c := r.skipSpace()
			r.i++
			if c == ']' {
				break
			}
			if c != ',' {
				r.i--
				return t, r.fail("want , or ]")
			}
		}
	}
	if r.skipSpace() != 0 || r.err != nil {
		if r.err != nil {
			return t, r.err
		}
		return t, r.fail("data after the log")
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
