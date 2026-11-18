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
		// DetectContentType reads at most the first 512 bytes.
		n := copy(e.sniff[:], r.Body)
		out = append(out, http.DetectContentType(e.sniff[:n])...)
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
