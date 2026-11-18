package http

import (
	"net/http"
	"strconv"
	"sync/atomic"
	"time"
)

// engineFastWriter selects how the engine writes a handler's buffered
// answer (HTTP-FAST-c2). true: the common answer is written in one
// append to one buffer and one write to the connection (fastAnswer), with
// no header map, no bufio layer and the Date line formatted once a
// second; any other answer takes the port of net/http's writer
// (engineResponse). false: every answer takes the port, the reference.
// Both give net/http's bytes (TestHTTPEngineMatchesNetHTTP); go test
// -args -http.fastwriter=false runs the package's tests on the port.
var engineFastWriter = true

// engineFastHook, when set, is told whether each buffered answer the
// engine wrote took the fast path. Only a test sets it.
var engineFastHook func(fast bool)

// engineFastBody is the longest body fastAnswer writes: net/http's
// response buffer, the longest body net/http still frames with a
// Content-Length it computes, not chunks.
const engineFastBody = 2048

// fastAnswer writes r, a handler's buffered answer, as net/http's writer
// would write it through httpWriteAnswer, when r is a common answer:
//   - nothing was written on w yet, and the request's body was read;
//   - a status from 200 to 999 that may have a body (not 204 or 304);
//   - a body of at most engineFastBody bytes;
//   - fields whose names are letters, digits and '-', none of the names
//     net/http's writer reads (Content-Length, Transfer-Encoding,
//     Connection, Trailer, Date), and whose values have no CR or LF and no
//     space or tab at either end.
//
// It reports false, having written nothing, for any other answer. The
// bytes are net/http's, in its order: the status line; the fields by
// canonical name, sorted, a name's values in their order; then Date,
// Content-Length (the body's, which HEAD declares only when it is not
// empty), the sniffed Content-Type when no field gives a type or an
// encoding, and Connection: close when the request asked for it. An
// empty body drops the fields' Content-Type, as httpWriteAnswer does.
func (w *engineResponse) fastAnswer(r HTTPResponse) bool {
	if w.wroteHeader || w.calledHeader || w.bodyFailed {
		return false
	}
	code := r.Status
	if code < 200 || code > 999 || code == 204 || code == 304 || len(r.Body) > engineFastBody {
		return false
	}
	e := w.e
	order := e.order[:0]
	haveType, haveEncoding := false, false
	for i, f := range r.Header {
		if !fastName(f.Name) || !fastValue(f.Value) {
			return false
		}
		name := canonicalName(f.Name)
		switch name {
		case "Content-Length", "Transfer-Encoding", "Connection", "Trailer", "Date":
			return false
		case "Content-Type":
			if r.Body == "" {
				continue
			}
			haveType = true
		case "Content-Encoding":
			haveEncoding = true
		}
		// Insertion by name, after the equal names: net/http's sorted keys,
		// each key's values in the order they were added.
		at := len(order)
		order = append(order, fastField{})
		for at > 0 && order[at-1].name > name {
			order[at] = order[at-1]
			at--
		}
		order[at] = fastField{name: name, i: i}
	}
	e.order = order

	out := append(e.out[:0], "HTTP/1.1 "...)
	out = strconv.AppendInt(out, int64(code), 10)
	if text := http.StatusText(code); text != "" {
		out = append(out, ' ')
		out = append(out, text...)
	} else {
		out = append(out, " status code "...)
		out = strconv.AppendInt(out, int64(code), 10)
	}
	out = append(out, "\r\n"...)
	for _, f := range order {
		out = append(out, f.name...)
		out = append(out, ": "...)
		out = append(out, r.Header[f.i].Value...)
		out = append(out, "\r\n"...)
	}
	out = append(out, "Date: "...)
	out = appendEngineDate(out, time.Now())
	out = append(out, "\r\n"...)
	w.contentLength = -1
	if !w.head || r.Body != "" {
		w.contentLength = int64(len(r.Body))
		out = append(out, "Content-Length: "...)
		out = strconv.AppendInt(out, int64(len(r.Body)), 10)
		out = append(out, "\r\n"...)
	}
	if !haveType && !haveEncoding && r.Body != "" {
		out = append(out, "Content-Type: "...)
		out = append(out, e.sniff.sniffType(r.Body)...)
		out = append(out, "\r\n"...)
	}
	if w.wantsClose {
		w.closeAfterReply = true
		out = append(out, "Connection: close\r\n"...)
	}
	out = append(out, "\r\n"...)
	if !w.head {
		out = append(out, r.Body...)
	}
	e.out = out

	w.wroteHeader, w.cw.wroteHeader = true, true
	w.status = code
	w.written = int64(len(r.Body))
	// A failed write is the connection's (engineWriter keeps it), so the
	// connection is not used again.
	if e.bufw.Buffered() == 0 {
		_, _ = engineWriter{e}.Write(out)
	} else {
		_, _ = e.bufw.Write(out)
	}
	return true
}

// fastField is a field of fastAnswer by its canonical name and its index
// in the answer's fields.
type fastField struct {
	name string
	i    int
}

// fastName reports whether name is a non-empty run of letters, digits and
// '-', which net/http writes in its canonical form.
func fastName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// fastValue reports whether net/http writes v as it is: no CR or LF,
// which it turns into spaces, and no space or tab at either end, which it
// trims.
func fastValue(v string) bool {
	if v != "" && (v[0] == ' ' || v[0] == '\t' || v[len(v)-1] == ' ' || v[len(v)-1] == '\t') {
		return false
	}
	for i := 0; i < len(v); i++ {
		if v[i] == '\r' || v[i] == '\n' {
			return false
		}
	}
	return true
}

// engineDate is the Date value of the current second, formatted once.
type engineDate struct {
	sec  int64
	text [len(http.TimeFormat)]byte
}

var engineDateNow atomic.Pointer[engineDate]

// appendEngineDate appends now as net/http writes Date (http.TimeFormat,
// UTC), formatting it only when the second changed.
func appendEngineDate(b []byte, now time.Time) []byte {
	sec := now.Unix()
	d := engineDateNow.Load()
	if d == nil || d.sec != sec {
		d = &engineDate{sec: sec}
		now.UTC().AppendFormat(d.text[:0], http.TimeFormat)
		engineDateNow.Store(d)
	}
	return append(b, d.text[:]...)
}

// sniffHead bounds how far past the first non-whitespace byte any of
// net/http's sniff signatures but text's reads: the longest is the
// 36-byte ms-fontobject mask, and the tag byte after <!DOCTYPE HTML
// the longest that skips whitespace. Only text's signature reads to
// the end, and mp4's reads a length it finds in the first 4 bytes.
const sniffHead = 64

// sniffLen is how much of a body net/http's sniff reads (its sniffLen).
const sniffLen = 512

// engineSniffHead selects how fastAnswer sniffs a body's type
// (HTTP-FAST-c7). true: the type of the head (the whitespace and
// sniffHead bytes past it) is net/http's DetectContentType of it, kept
// for the next answer on the connection with the same head; when it is
// plain text, the rest is checked for a byte text's signature refuses,
// in words of 8; any other head is sniffed whole, as net/http does.
// false: DetectContentType of the 512 bytes, the reference. Both give
// DetectContentType's answer (TestSniffIsNetHTTPs).
var engineSniffHead = true

// engineSniffHook, when set, is told how each body was sniffed: "kept"
// (the head's type was the last answer's), "head" (the head was
// sniffed and decided), "whole" (the 512 bytes were sniffed). Only a
// test sets it.
var engineSniffHook func(how string)

// engineSniff is a connection's sniff: the last head sniffed and its
// type, and the room the whole 512 bytes are copied to.
type engineSniff struct {
	head  [sniffLen]byte
	headN int
	ct    string // "": no head kept
	whole [sniffLen]byte
}

// sniffType is http.DetectContentType(b), with b's bytes copied, never
// converted.
//
// DetectContentType's head decides the whole when it is plain text and
// nothing in the rest is a byte text's signature refuses: every other
// signature reads only the head (mp4's too, since plain text starts
// with no zero byte, so the box length it reads is above 512), and
// text's signature passes the head and the rest. The type of a head is
// DetectContentType's of its bytes, so a head equal to the last one has
// its type.
func (s *engineSniff) sniffType(b string) string {
	if len(b) > sniffLen {
		b = b[:sniffLen]
	}
	ws := 0
	for ws < len(b) && sniffWS(b[ws]) {
		ws++
	}
	if engineSniffHead {
		head := b[:min(len(b), ws+sniffHead)]
		how := "kept"
		if s.ct == "" || s.headN != len(head) || string(s.head[:s.headN]) != head {
			s.headN = copy(s.head[:], head)
			s.ct = http.DetectContentType(s.head[:s.headN])
			how = "head"
		}
		if len(head) == len(b) || s.ct == "text/plain; charset=utf-8" && sniffText(b[len(head):]) {
			if engineSniffHook != nil {
				engineSniffHook(how)
			}
			return s.ct
		}
	}
	if engineSniffHook != nil {
		engineSniffHook("whole")
	}
	n := copy(s.whole[:], b)
	return http.DetectContentType(s.whole[:n])
}

// sniffWS is the mime sniff's whitespace, as net/http skips it.
func sniffWS(c byte) bool {
	return c == '\t' || c == '\n' || c == '\x0c' || c == '\r' || c == ' '
}

// sniffText reports whether b holds no byte net/http's text signature
// refuses: 0x00-0x08, 0x0B, 0x0E-0x1A and 0x1C-0x1F. A word of 8 bytes
// with none below 0x20 passes in one test; a word with one is read a
// byte at a time.
func sniffText(b string) bool {
	const ones, highs = 0x0101010101010101, 0x8080808080808080
	i := 0
	for ; i+8 <= len(b); i += 8 {
		w := uint64(b[i]) | uint64(b[i+1])<<8 | uint64(b[i+2])<<16 | uint64(b[i+3])<<24 |
			uint64(b[i+4])<<32 | uint64(b[i+5])<<40 | uint64(b[i+6])<<48 | uint64(b[i+7])<<56
		if (w-0x20*ones)&^w&highs == 0 {
			continue
		}
		for _, c := range []byte{b[i], b[i+1], b[i+2], b[i+3], b[i+4], b[i+5], b[i+6], b[i+7]} {
			if sniffRefused(c) {
				return false
			}
		}
	}
	for ; i < len(b); i++ {
		if sniffRefused(b[i]) {
			return false
		}
	}
	return true
}

// sniffRefused is a byte net/http's text signature refuses.
func sniffRefused(c byte) bool {
	return c < 0x20 && c != '\t' && c != '\n' && c != '\x0c' && c != '\r' && c != 0x1B
}
