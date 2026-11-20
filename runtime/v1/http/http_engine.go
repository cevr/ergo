package http

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"ergo.local/prototype/runtime/rt"
)

// serveEngine selects how Serve reads its requests (HTTP-FAST-c1). true:
// Serve's own HTTP/1.1 engine reads each connection (serveConn), and
// hands net/http the connections it does not read itself. false: net/http
// reads every connection, the reference. Both give the same bytes for the
// same input (TestHTTPEngineMatchesNetHTTP); go test -args
// -http.engine=false runs the package's tests on the reference.
var serveEngine = true

// engineLazyWatch selects when the engine starts its background read of a
// request's connection, the read that sees the client leave
// (HTTP-FAST-c3). true: at the handler's first interruption point, a wait,
// Check or Stopped (rt.WhenPolled, HTTP-FAST-c4), the first point a stop
// can end it, so a handler that answers without one costs no goroutine,
// deadline or read. false: before the handler
// runs, as net/http starts it, the reference. go test -args
// -http.lazywatch=false runs the package's tests on the reference.
var engineLazyWatch = true

// The engine reads the common request itself and leaves the rest to
// net/http: a head of at most engineHead bytes whose lines are all
// "Name: value" over CRLF, an HTTP/1.1 request line with an origin-form
// target, one Host, and no body or one of a declared Content-Length within
// MaxBodyBytes. Any other head (chunked, Expect, Trailer, Pragma, a
// CONNECT, HTTP/1.0, bytes net/http might read another way, an error to
// answer) goes to net/http whole, with the bytes read so far put back, the
// moment the engine sees it, so net/http reads it as it would have.
const engineHead = 4096

// maxHeaderLines is net/http's DefaultMaxHeaderValueCount: past it,
// net/http refuses the head (431), so the engine hands such a head over.
const maxHeaderLines = 500

// maxPostHandlerReadBytes is net/http's bound on a request body it reads
// after the handler to keep the connection.
const maxPostHandlerReadBytes = 256 << 10

// rstAvoidanceDelay is net/http's wait between its half close and its
// close of a connection whose request body it could not read.
const rstAvoidanceDelay = 500 * time.Millisecond

// engineListener is the listener net/http's Server serves when the engine
// reads requests: it accepts nothing itself. Its accept loop (accept)
// takes each connection from the transport's listener for the engine,
// and gives net/http only the connections the engine hands over and the
// accept errors, so net/http's Serve still returns, and backs off, on the
// transport's own errors.
type engineListener struct {
	net.Listener
	s       *httpServer
	errs    chan error
	handoff chan *httpConn
	closed  chan struct{}
	once    sync.Once
}

func newEngineListener(l net.Listener, s *httpServer) *engineListener {
	return &engineListener{Listener: l, s: s, errs: make(chan error), handoff: make(chan *httpConn), closed: make(chan struct{})}
}

func (l *engineListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.handoff:
		return c, nil
	case err := <-l.errs:
		return nil, err
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *engineListener) Close() error {
	err := net.ErrClosed
	l.once.Do(func() {
		close(l.closed)
		err = l.Listener.Close()
	})
	return err
}

// accept runs from Serve's start until the listener closes, as a worker:
// each accepted connection gets its engine goroutine (serveConn).
func (l *engineListener) accept() {
	defer l.s.workers.Done()
	for {
		nc, err := l.Listener.Accept()
		if err != nil {
			select {
			case l.errs <- err:
			case <-l.closed:
				return
			}
			// net/http's Serve retries a temporary error after its
			// back-off, and returns on any other.
			if ne, ok := err.(interface{ Temporary() bool }); ok && ne.Temporary() {
				continue
			}
			return
		}
		c := l.s.newConn(nc)
		s := l.s
		s.mu.Lock()
		if s.closing {
			s.mu.Unlock()
			_ = nc.Close()
			continue
		}
		s.workers.Add(1)
		s.conns[c] = struct{}{}
		s.mu.Unlock()
		go s.serveConn(c, l)
	}
}

// handOver gives c to net/http with the bytes the engine read put back,
// and reports whether net/http took it (it does not once Serve stops).
func (l *engineListener) handOver(c *httpConn, read []byte) bool {
	c.replay = read
	c.handed = true
	select {
	case l.handoff <- c:
		return true
	case <-l.closed:
		return false
	}
}

// engineConn is the engine's state of one connection: the read buffer
// (buf[r:w] read and not yet used), net/http's 4 KiB write buffer over the
// connection (bufw, werr), the last request's method, and the response,
// reused by each request.
type engineConn struct {
	c          *httpConn
	s          *httpServer
	buf        [engineHead]byte
	r, w       int
	rerr       error // a read error that came with bytes, for the next read
	bufw       *bufio.Writer
	werr       error
	lastMethod string
	bg         chan int    // the background read's byte count
	watching   bool        // the background read of this request runs (watch)
	watchFn    func()      // watch, made once for the connection's requests
	poll       rt.PollHook // the storage of the requests' watch hook (answer)
	// The watcher's state (http_peer.go): its registration (peer, rc,
	// pfd; peerFailed: none can be made), the armed watch (armed, guarded
	// by c.mu), an end of stream seen (eof) and a close that may wait
	// behind bytes (pending).
	peer       *peerWatcher
	rc         syscall.RawConn
	pfd        int32
	peerFailed bool
	armed      bool
	eof        atomic.Bool
	pending    atomic.Bool
	resp       engineResponse
	fields     []fieldAt
	out        []byte      // fastAnswer's answer, reused
	order      []fastField // fastAnswer's fields by name, reused
	sniff      engineSniff // fastAnswer's sniff
}

// fieldAt is a header line of the head being read, by offsets into buf.
type fieldAt struct {
	name, nameEnd, value, valueEnd int
	kind                           uint8 // fieldHost, fieldLength, ... (nameKind)
	canonical                      bool  // the name is in textproto's canonical form
}

type engineWriter struct{ e *engineConn }

// Write is net/http's checkConnErrorWriter: the first failed write is the
// connection's, and the connection is not used again.
func (w engineWriter) Write(p []byte) (int, error) {
	n, err := w.e.c.Conn.Write(p)
	if err != nil && w.e.werr == nil {
		w.e.werr = err
	}
	return n, err
}

// fill reads more of the connection into buf. false: the read failed.
func (e *engineConn) fill() bool {
	if e.rerr != nil {
		return false
	}
	n, err := e.c.Read(e.buf[e.w:])
	e.w += n
	if err != nil {
		// The bytes that came with it are read; the next fill fails.
		e.rerr = err
		return n > 0
	}
	return true
}

// compact moves the unread bytes to the front of buf.
func (e *engineConn) compact() {
	if e.r > 0 {
		e.w = copy(e.buf[:], e.buf[e.r:e.w])
		e.r = 0
	}
}

// The ends of readHead.
const (
	headRead   = iota // buf[r:end] is a head the engine reads
	headClosed        // the connection ends quietly, as net/http ends it
	headHanded        // net/http takes the connection
)

// request is a head the engine read: the handler's arguments and what
// net/http keeps of it for the response.
type request struct {
	method, path, rawPath, rawQuery string
	fields                          []HTTPField
	contentLength                   int64
	wantsClose                      bool
}

// readHead reads the next request's head into buf, line by line, and
// checks each whole line as it comes: a line the engine does not read
// itself hands the connection over at once, so a client that waits after
// it gets net/http's answer, not the engine's wait. first is the
// connection's first request: before any other, net/http waits for four
// bytes (its idle Peek) and ends the connection quietly when a read fails
// before them, and after a POST it drops up to four leading CR or LF.
func (e *engineConn) readHead(first bool, req *request) int {
	e.compact()
	if !first {
		for e.w < 4 {
			if !e.fill() {
				return headClosed
			}
		}
		if e.lastMethod == "POST" {
			n := 0
			for n < 4 && (e.buf[n] == '\r' || e.buf[n] == '\n') {
				n++
			}
			e.r = n
		}
	}
	e.fields = e.fields[:0]
	var hosts, lengths int
	host, length := -1, -1
	line, scan := e.r, e.r
	for {
		i := bytes.IndexByte(e.buf[scan:e.w], '\n')
		if i < 0 {
			scan = e.w
			if e.w == len(e.buf) {
				return headHanded
			}
			if !e.fill() {
				if e.w > e.r {
					return headHanded
				}
				return headClosed
			}
			continue
		}
		end := scan + i
		scan = end + 1
		if end == line || e.buf[end-1] != '\r' {
			return headHanded // a bare LF, or an empty request line
		}
		text := e.buf[line : end-1]
		start := line
		line = scan
		if start == e.r {
			if !requestLine(text) {
				return headHanded
			}
			continue
		}
		if len(text) == 0 {
			break
		}
		f, ok := fieldLine(text, start)
		if !ok || len(e.fields) == maxHeaderLines {
			return headHanded
		}
		switch f.kind {
		case fieldHost:
			hosts++
			host = len(e.fields)
		case fieldLength:
			lengths++
			length = len(e.fields)
		case fieldNetHTTP:
			return headHanded
		}
		e.fields = append(e.fields, f)
	}
	end := scan
	if hosts != 1 || lengths > 1 || !validHost(e.buf[e.fields[host].value:e.fields[host].valueEnd]) {
		return headHanded
	}
	if length >= 0 {
		n, ok := declaredLength(e.buf[e.fields[length].value:e.fields[length].valueEnd])
		if !ok || n > int64(e.s.limits.MaxBodyBytes) {
			return headHanded
		}
		req.contentLength = n
	} else {
		req.contentLength = 0
	}
	// One string holds the head; the arguments are its parts.
	head := string(e.buf[e.r:end])
	at := func(i, j int) string { return head[i-e.r : j-e.r] }
	sp := bytes.IndexByte(e.buf[e.r:end], ' ') + e.r
	req.method = at(e.r, sp)
	target := at(sp+1, sp+1+bytes.IndexByte(e.buf[sp+1:end], ' '))
	raw, query, _ := strings.Cut(target, "?")
	req.rawPath, req.rawQuery, req.path = raw, query, raw
	if strings.IndexByte(raw, '%') >= 0 {
		req.path, _ = url.PathUnescape(raw)
	}
	req.fields = make([]HTTPField, 0, len(e.fields)-1)
	var connection []string
	for i, f := range e.fields {
		if i == host {
			continue
		}
		field := HTTPField{Name: at(f.name, f.nameEnd), Value: at(f.value, f.valueEnd)}
		if !f.canonical {
			field.Name = textproto.CanonicalMIMEHeaderKey(field.Name)
		}
		if f.kind == fieldConnection {
			connection = append(connection, field.Value)
		}
		req.fields = append(req.fields, field)
	}
	// net/http's Request.Close and response.wantsClose.
	req.wantsClose = valuesContainToken(connection, "close") || len(connection) > 0 && hasToken(connection[0], "close")
	sortFields(req.fields)
	e.r = end
	return headRead
}

// requestLine reports whether line is a request line the engine reads:
// METHOD SP origin-form-target SP HTTP/1.1, the method upper-case
// letters and not CONNECT, the path's bytes ones whose escaped form Go
// keeps as they are (EscapedPath is the raw path), the query printable
// ASCII but '#'.
func requestLine(line []byte) bool {
	sp := bytes.IndexByte(line, ' ')
	if sp <= 0 {
		return false
	}
	for _, c := range line[:sp] {
		if headBytes[c]&byteUpper == 0 {
			return false
		}
	}
	if string(line[:sp]) == "CONNECT" {
		return false
	}
	rest := line[sp+1:]
	sp = bytes.IndexByte(rest, ' ')
	if sp <= 0 || string(rest[sp+1:]) != "HTTP/1.1" || rest[0] != '/' {
		return false
	}
	target := rest[:sp]
	i := 0
	for ; i < len(target) && target[i] != '?'; i++ {
		c := target[i]
		switch {
		case c == '%':
			if i+2 >= len(target) || !isHex(target[i+1]) || !isHex(target[i+2]) {
				return false
			}
			i += 2
		case headBytes[c]&bytePath == 0:
			return false
		}
	}
	for _, c := range target[min(i+1, len(target)):] {
		if headBytes[c]&byteQuery == 0 {
			return false
		}
	}
	return true
}

// The classes of headBytes, the bytes each part of a head the engine
// reads may hold (HTTP-FAST-c8: one table load per byte).
const (
	byteUpper = 1 << iota // a method's: 'A' to 'Z'
	bytePath              // a path's: an unreserved byte, a sub-delim, ':', '@' or '/', which net/url neither escapes nor refuses
	byteQuery             // a query's: printable ASCII but '#'
	byteName              // a field name's: a letter, a digit or '-'
	byteValue             // a field value's: visible ASCII, space or tab
	byteHost              // a Host's: a letter, a digit or one of ".-_:[]"
	byteLower             // 'a' to 'z'
)

// headBytes is each byte's classes.
var headBytes = func() (t [256]uint8) {
	for i := range t {
		c := byte(i)
		upper, lower, digit := 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9'
		alnum := upper || lower || digit
		set := func(class uint8, in bool) {
			if in {
				t[i] |= class
			}
		}
		set(byteUpper, upper)
		set(byteLower, lower)
		set(bytePath, alnum || strings.IndexByte("-._~/!$&'()*+,;=:@", c) >= 0)
		set(byteQuery, c > ' ' && c < 0x7f && c != '#')
		set(byteName, alnum || c == '-')
		set(byteValue, c == '\t' || c >= ' ' && c < 0x7f)
		set(byteHost, alnum || strings.IndexByte(".-_:[]", c) >= 0)
	}
	return t
}()

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

// fieldLine reads "Name: value" from line, which starts at offset start
// of buf: a name of letters, digits and '-', and a value of visible ASCII,
// spaces and tabs, without its leading and trailing ones.
func fieldLine(line []byte, start int) (fieldAt, bool) {
	colon := bytes.IndexByte(line, ':')
	if colon <= 0 {
		return fieldAt{}, false
	}
	// One pass over the name checks its bytes and whether it is in
	// textproto's canonical form: upper case first and after each '-',
	// lower case elsewhere.
	canonical, upper := true, true
	for _, c := range line[:colon] {
		class := headBytes[c]
		if class&byteName == 0 {
			return fieldAt{}, false
		}
		if upper && class&byteLower != 0 || !upper && class&byteUpper != 0 {
			canonical = false
		}
		upper = c == '-'
	}
	v, ve := colon+1, len(line)
	for _, c := range line[v:] {
		if headBytes[c]&byteValue == 0 {
			return fieldAt{}, false
		}
	}
	for v < ve && (line[v] == ' ' || line[v] == '\t') {
		v++
	}
	for ve > v && (line[ve-1] == ' ' || line[ve-1] == '\t') {
		ve--
	}
	return fieldAt{start, start + colon, start + v, start + ve, nameKind(line[:colon]), canonical}, true
}

// The names readHead acts on.
const (
	fieldOther      uint8 = iota
	fieldHost             // Host
	fieldLength           // Content-Length
	fieldConnection       // Connection
	fieldNetHTTP          // Transfer-Encoding, Expect, Trailer, Pragma: net/http reads the request
)

// nameKind is which of readHead's names name is, ASCII case aside; name
// holds letters, digits and '-' only (fieldLine checked it), so its
// length picks the one or two names it can be.
func nameKind(name []byte) uint8 {
	switch len(name) {
	case 4:
		if foldName(name, "host") {
			return fieldHost
		}
	case 6:
		if foldName(name, "expect") || foldName(name, "pragma") {
			return fieldNetHTTP
		}
	case 7:
		if foldName(name, "trailer") {
			return fieldNetHTTP
		}
	case 10:
		if foldName(name, "connection") {
			return fieldConnection
		}
	case 14:
		if foldName(name, "content-length") {
			return fieldLength
		}
	case 17:
		if foldName(name, "transfer-encoding") {
			return fieldNetHTTP
		}
	}
	return fieldOther
}

// foldName reports whether name, of letters, digits and '-', is lower
// (lower-case letters and '-', of the same length) ASCII case aside:
// setting bit 0x20 lower-cases a letter and keeps a digit or '-'.
func foldName(name []byte, lower string) bool {
	for i, c := range name {
		if c|0x20 != lower[i] {
			return false
		}
	}
	return true
}

// validHost: a Host of names, addresses and ports only.
func validHost(h []byte) bool {
	for _, c := range h {
		if headBytes[c]&byteHost == 0 {
			return false
		}
	}
	return true
}

// sortFields sorts fields by name, keeping the arrival order of equal
// names, as httpFields gives net/http's: an insertion sort over the few
// fields a request has, slices' stable sort over many.
func sortFields(fields []HTTPField) {
	if len(fields) > 16 {
		slices.SortStableFunc(fields, func(a, b HTTPField) int { return strings.Compare(a.Name, b.Name) })
		return
	}
	for i := 1; i < len(fields); i++ {
		for j := i; j > 0 && fields[j].Name < fields[j-1].Name; j-- {
			fields[j], fields[j-1] = fields[j-1], fields[j]
		}
	}
}

// declaredLength is a Content-Length of one to eighteen digits.
func declaredLength(v []byte) (int64, bool) {
	if len(v) == 0 || len(v) > 18 {
		return 0, false
	}
	var n int64
	for _, c := range v {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int64(c-'0')
	}
	return n, true
}

// canonicalName is textproto's canonical form of a name of letters,
// digits and '-': upper case first and after each '-', lower case
// elsewhere. A name already in it is returned as it is.
func canonicalName(s string) string {
	upper := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		if upper && 'a' <= c && c <= 'z' || !upper && 'A' <= c && c <= 'Z' {
			return textproto.CanonicalMIMEHeaderKey(s)
		}
		upper = c == '-'
	}
	return s
}

// valuesContainToken is httpguts.HeaderValuesContainsToken: some value
// has token as one of its comma-separated elements.
func valuesContainToken(values []string, token string) bool {
	for _, v := range values {
		for {
			elem, rest, more := strings.Cut(v, ",")
			if strings.EqualFold(strings.Trim(elem, " \t"), token) {
				return true
			}
			if !more {
				break
			}
			v = rest
		}
	}
	return false
}

// hasToken is net/http's hasToken: token in v between spaces, commas or
// tabs, ASCII case aside.
func hasToken(v, token string) bool {
	if len(token) > len(v) || token == "" {
		return false
	}
	if v == token {
		return true
	}
	boundary := func(b byte) bool { return b == ' ' || b == ',' || b == '\t' }
	for sp := 0; sp <= len(v)-len(token); sp++ {
		if b := v[sp]; b != token[0] && b|0x20 != token[0] {
			continue
		}
		if sp > 0 && !boundary(v[sp-1]) {
			continue
		}
		if end := sp + len(token); end != len(v) && !boundary(v[end]) {
			continue
		}
		if strings.EqualFold(v[sp:sp+len(token)], token) {
			return true
		}
	}
	return false
}

// readBody reads the declared body: n bytes, from buf and then the
// connection. A read that fails ends it with that failure (io.EOF is
// io.ErrUnexpectedEOF, as net/http's body has it) and the count of
// declared bytes not read; every byte read then was the body's.
func (e *engineConn) readBody(n int64) (string, int64, error) {
	if int64(e.w-e.r) >= n {
		body := string(e.buf[e.r : e.r+int(n)])
		e.r += int(n)
		return body, 0, nil
	}
	fail := func(got int64) (string, int64, error) {
		e.r = e.w
		err := e.rerr
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return "", n - got, err
	}
	if n <= engineHead {
		e.compact()
		for int64(e.w) < n {
			if !e.fill() {
				return fail(int64(e.w))
			}
		}
		body := string(e.buf[:n])
		e.r = int(n)
		return body, 0, nil
	}
	var body strings.Builder
	body.Grow(int(n))
	body.Write(e.buf[e.r:e.w])
	got := int64(e.w - e.r)
	e.r, e.w = 0, 0
	for got < n {
		m, err := e.c.Read(e.buf[:min(int64(len(e.buf)), n-got)])
		body.Write(e.buf[:m])
		got += int64(m)
		if err != nil {
			e.rerr = err
			if got < n {
				return fail(got)
			}
		}
	}
	return body.String(), 0, nil
}

// startBackgroundRead is net/http's background read: while the handler
// runs, a read of the connection sees the client leave (httpConn.Read
// stops the request) or keeps the next request's first bytes.
func (e *engineConn) startBackgroundRead() bool {
	e.compact()
	if e.w == len(e.buf) || e.rerr != nil {
		return false
	}
	go func() {
		n, err := e.c.Read(e.buf[e.w:])
		if err != nil && !isTimeout(err) && e.rerr == nil {
			e.rerr = err
		}
		e.bg <- n
	}()
	return true
}

// watch starts the background read of the request being answered, once
// (watching), when the connection has room to read into.
func (e *engineConn) watch() {
	if e.watching || e.armed {
		return
	}
	if engineWatcher && e.registerPeer() {
		e.arm()
	} else {
		e.watching = e.startBackgroundRead()
	}
	if (e.watching || e.armed) && engineWatchHook != nil {
		engineWatchHook()
	}
}

// unwatch ends the watch of the request answered, the background read or
// the watcher's.
func (e *engineConn) unwatch() {
	if e.watching {
		e.abortBackgroundRead()
	}
	if e.armed {
		e.disarm()
	}
}

// engineWatchHook, when set, is called for each background read the
// engine starts. Only a test sets it.
var engineWatchHook func()

// abortBackgroundRead ends the background read, as net/http does, with a
// past read deadline, and keeps what it read.
func (e *engineConn) abortBackgroundRead() {
	_ = e.c.SetReadDeadline(aLongTimeAgo)
	e.w += <-e.bg
	_ = e.c.SetReadDeadline(time.Time{})
}

// close is net/http's conn.close: the buffered bytes go out, then the
// connection closes.
func (e *engineConn) close() {
	_ = e.bufw.Flush()
	e.c.enter(phaseClosed)
	_ = e.c.Conn.Close()
}

// serveConn is the engine's goroutine of c: it reads each request, runs
// its handler on this goroutine, as net/http runs it on the goroutine
// that read it, and writes the answer as net/http writes it
// (engineResponse), with net/http's phases (connState's), until the
// connection ends or a head goes to net/http.
func (s *httpServer) serveConn(c *httpConn, l *engineListener) {
	e := &engineConn{c: c, s: s, bg: make(chan int, 1)}
	e.bufw = bufio.NewWriterSize(engineWriter{e}, 4<<10)
	e.watchFn = e.watch
	handed := false
	defer func() {
		e.unregisterPeer()
		if !handed {
			e.close()
		}
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
		s.workers.Done()
	}()
	s.watch(c)
	c.enter(phaseHeader)
	var req request
	for first := true; ; first = false {
		switch e.readHead(first, &req) {
		case headClosed:
			return
		case headHanded:
			e.unregisterPeer() // net/http watches the connections it serves
			handed = l.handOver(c, e.buf[e.r:e.w])
			return
		}
		s.mu.Lock()
		closing := s.closing
		s.mu.Unlock()
		if closing {
			return
		}
		e.lastMethod = req.method
		c.enter(phaseFree)
		in := httpIncoming{Method: req.method, Path: req.path, RawPath: req.rawPath, Header: req.fields}
		if req.rawQuery != "" {
			in.Query = httpParseQuery(req.rawQuery)
		}
		w := &e.resp
		w.reset(e, req.method == "HEAD", req.wantsClose, req.contentLength > 0)
		if req.contentLength > 0 {
			c.enter(phaseBody)
			body, unread, err := e.readBody(req.contentLength)
			c.enter(phaseFree)
			if err != nil {
				in.ReadError = err.Error()
				w.bodyFailed, w.bodyUnread = true, unread
			} else {
				in.Body = body
			}
		}
		watch := in.ReadError == ""
		e.watching = false
		if watch && !engineLazyWatch {
			// The reference: net/http's background read from before the
			// handler runs.
			e.watch()
		}
		c.mu.Lock()
		gone := c.gone
		c.mu.Unlock()
		if watch && gone || !s.answerOn(w, &in, c, watch) {
			e.unwatch()
			return
		}
		w.finishRequest()
		e.unwatch()
		if !w.shouldReuse() {
			if w.bodyLimitHit {
				// net/http's closeWriteAndWait.
				_ = e.bufw.Flush()
				_ = c.CloseWrite()
				time.Sleep(rstAvoidanceDelay)
			}
			return
		}
		c.enter(phaseIdle)
		s.mu.Lock()
		closing = s.closing
		s.mu.Unlock()
		if closing {
			return
		}
	}
}

// answerOn runs answer, and ends a panic of the answer's writing (an
// invalid status) as net/http's conn.serve ends it: logged, and the
// connection closed.
func (s *httpServer) answerOn(w *engineResponse, in *httpIncoming, c *httpConn, watch bool) (ok bool) {
	defer func() {
		if err := recover(); err != nil {
			const size = 64 << 10
			buf := make([]byte, size)
			buf = buf[:runtime.Stack(buf, false)]
			log.Printf("http: panic serving %v: %v\n%s", c.RemoteAddr(), err, buf)
			ok = false
		}
	}()
	return s.answer(w, in, c, watch)
}

// engineResponse is net/http's response and chunkWriter (server.go) for
// the requests the engine reads: HTTP/1.1, never CONNECT, no Expect,
// keep-alives on. Its rules are net/http's, in net/http's order, over the
// same 2 KiB buffer before chunking and 4 KiB connection buffer, so an
// answer's bytes are net/http's (TestHTTPEngineMatchesNetHTTP).
type engineResponse struct {
	e               *engineConn
	head            bool
	wantsClose      bool
	bodyDeclared    bool  // the request declared a body
	bodyFailed      bool  // and reading it failed
	bodyUnread      int64 // with this many declared bytes not read
	handlerHeader   http.Header
	calledHeader    bool
	wroteHeader     bool
	status          int
	contentLength   int64
	written         int64
	handlerDone     bool
	closeAfterReply bool
	bodyLimitHit    bool
	trailers        []string
	w               *bufio.Writer
	cw              engineChunkWriter
	scratch         [20]byte // a status code, or a chunk's size
	clenBuf         [20]byte
	dateBuf         [len(http.TimeFormat)]byte
}

type engineChunkWriter struct {
	res         *engineResponse
	header      http.Header
	wroteHeader bool
	chunking    bool
}

func (w *engineResponse) reset(e *engineConn, head, wantsClose, body bool) {
	header := w.handlerHeader
	if header == nil {
		header = make(http.Header)
	} else {
		clear(header)
	}
	bw := w.w
	*w = engineResponse{e: e, head: head, wantsClose: wantsClose, bodyDeclared: body, handlerHeader: header, contentLength: -1, trailers: w.trailers[:0]}
	w.cw.res = w
	if bw == nil {
		bw = bufio.NewWriterSize(&w.cw, 2048)
	} else {
		bw.Reset(&w.cw)
	}
	w.w = bw
}

func (w *engineResponse) Header() http.Header {
	if w.cw.header == nil && w.wroteHeader && !w.cw.wroteHeader {
		w.cw.header = w.handlerHeader.Clone()
	}
	w.calledHeader = true
	return w.handlerHeader
}

func (w *engineResponse) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	if code < 100 || code > 999 {
		panic(fmt.Sprintf("invalid WriteHeader code %v", code))
	}
	if code >= 100 && code <= 199 && code != http.StatusSwitchingProtocols {
		bw := w.e.bufw
		w.writeStatusLine(code)
		_ = w.handlerHeader.WriteSubset(bw, excludedHeadersNoBody)
		_, _ = bw.WriteString("\r\n")
		_ = bw.Flush()
		return
	}
	w.wroteHeader = true
	w.status = code
	if w.calledHeader && w.cw.header == nil {
		w.cw.header = w.handlerHeader.Clone()
	}
	if cl := headerGet(w.handlerHeader, "Content-Length"); cl != "" {
		v, err := strconv.ParseInt(cl, 10, 64)
		if err == nil && v >= 0 {
			w.contentLength = v
		} else {
			w.handlerHeader.Del("Content-Length")
		}
	}
}

var excludedHeadersNoBody = map[string]bool{"Content-Length": true, "Transfer-Encoding": true}

func (w *engineResponse) Write(p []byte) (int, error) { return w.write(len(p), p, "") }

func (w *engineResponse) WriteString(s string) (int, error) { return w.write(len(s), nil, s) }

func (w *engineResponse) write(lenData int, dataB []byte, dataS string) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if lenData == 0 {
		return 0, nil
	}
	if !bodyAllowedForStatus(w.status) {
		return 0, http.ErrBodyNotAllowed
	}
	w.written += int64(lenData)
	if w.contentLength != -1 && w.written > w.contentLength {
		return 0, http.ErrContentLength
	}
	if dataB != nil {
		return w.w.Write(dataB)
	}
	return w.w.WriteString(dataS)
}

// FlushError is http.ResponseController's Flush.
func (w *engineResponse) FlushError() error {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	err := w.w.Flush()
	if !w.cw.wroteHeader {
		w.cw.writeHeader(nil)
	}
	if e2 := w.e.bufw.Flush(); err == nil {
		err = e2
	}
	return err
}

func (w *engineResponse) finishRequest() {
	w.handlerDone = true
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	_ = w.w.Flush()
	w.cw.close()
	_ = w.e.bufw.Flush()
}

func (w *engineResponse) shouldReuse() bool {
	if w.closeAfterReply || w.bodyLimitHit {
		return false
	}
	if !w.head && w.contentLength != -1 && bodyAllowedForStatus(w.status) && w.contentLength != w.written {
		return false
	}
	return w.e.werr == nil
}

func (w *engineResponse) writeStatusLine(code int) {
	bw := w.e.bufw
	_, _ = bw.WriteString("HTTP/1.1 ")
	if text := http.StatusText(code); text != "" {
		_, _ = bw.Write(strconv.AppendInt(w.scratch[:0], int64(code), 10))
		_ = bw.WriteByte(' ')
		_, _ = bw.WriteString(text)
		_, _ = bw.WriteString("\r\n")
	} else {
		_, _ = fmt.Fprintf(bw, "%03d status code %d\r\n", code, code)
	}
}

func (w *engineResponse) declareTrailer(k string) {
	k = http.CanonicalHeaderKey(k)
	if strings.HasPrefix(k, "If-") || badTrailer[k] {
		return
	}
	w.trailers = append(w.trailers, k)
}

// badTrailer is httpguts' list of the names a trailer may not have.
var badTrailer = map[string]bool{
	"Authorization": true, "Cache-Control": true, "Connection": true, "Content-Encoding": true,
	"Content-Length": true, "Content-Range": true, "Content-Type": true, "Expect": true,
	"Host": true, "Keep-Alive": true, "Max-Forwards": true, "Pragma": true,
	"Proxy-Authenticate": true, "Proxy-Authorization": true, "Proxy-Connection": true, "Range": true,
	"Realm": true, "Te": true, "Trailer": true, "Transfer-Encoding": true, "Www-Authenticate": true,
}

func (w *engineResponse) finalTrailers() http.Header {
	var t http.Header
	for k, vv := range w.handlerHeader {
		if kk, found := strings.CutPrefix(k, http.TrailerPrefix); found {
			if t == nil {
				t = make(http.Header)
			}
			t[kk] = vv
		}
	}
	for _, k := range w.trailers {
		if t == nil {
			t = make(http.Header)
		}
		for _, v := range w.handlerHeader[k] {
			t.Add(k, v)
		}
	}
	return t
}

func headerGet(h http.Header, key string) string {
	if v := h[key]; len(v) > 0 {
		return v[0]
	}
	return ""
}

func headerHas(h http.Header, key string) bool {
	_, ok := h[key]
	return ok
}

func bodyAllowedForStatus(status int) bool {
	switch {
	case status >= 100 && status <= 199:
		return false
	case status == 204, status == 304:
		return false
	}
	return true
}

func (cw *engineChunkWriter) Write(p []byte) (n int, err error) {
	if !cw.wroteHeader {
		cw.writeHeader(p)
	}
	if cw.res.head {
		return len(p), nil
	}
	bw := cw.res.e.bufw
	if cw.chunking {
		size := append(strconv.AppendInt(cw.res.scratch[:0], int64(len(p)), 16), '\r', '\n')
		if _, err = bw.Write(size); err != nil {
			_ = cw.res.e.c.Conn.Close()
			return
		}
	}
	n, err = bw.Write(p)
	if cw.chunking && err == nil {
		_, err = bw.WriteString("\r\n")
	}
	if err != nil {
		_ = cw.res.e.c.Conn.Close()
	}
	return
}

func (cw *engineChunkWriter) close() {
	if !cw.wroteHeader {
		cw.writeHeader(nil)
	}
	if cw.chunking {
		bw := cw.res.e.bufw
		_, _ = bw.WriteString("0\r\n")
		if trailers := cw.res.finalTrailers(); trailers != nil {
			_ = trailers.Write(bw)
		}
		_, _ = bw.WriteString("\r\n")
	}
}

// writeHeader is net/http's chunkWriter.writeHeader for an HTTP/1.1
// request that is not CONNECT and expects no 100-continue, with
// keep-alives on: it settles the length, the connection's reuse, the
// sniffed type and the framing, and writes the status line and header.
func (cw *engineChunkWriter) writeHeader(p []byte) {
	if cw.wroteHeader {
		return
	}
	cw.wroteHeader = true
	w := cw.res
	header := cw.header
	owned := header != nil
	if !owned {
		header = w.handlerHeader
	}
	var excludeHeader map[string]bool
	delHeader := func(key string) {
		if owned {
			header.Del(key)
			return
		}
		if _, ok := header[key]; !ok {
			return
		}
		if excludeHeader == nil {
			excludeHeader = make(map[string]bool)
		}
		excludeHeader[key] = true
	}
	var set extraHeader
	trailers := false
	for k := range cw.header {
		if strings.HasPrefix(k, http.TrailerPrefix) {
			if excludeHeader == nil {
				excludeHeader = make(map[string]bool)
			}
			excludeHeader[k] = true
			trailers = true
		}
	}
	for _, v := range cw.header["Trailer"] {
		trailers = true
		foreachHeaderElement(v, w.declareTrailer)
	}
	te := headerGet(header, "Transfer-Encoding")
	hasTE := te != ""
	if w.handlerDone && !trailers && !hasTE && bodyAllowedForStatus(w.status) && !headerHas(header, "Content-Length") && (!w.head || len(p) > 0) {
		w.contentLength = int64(len(p))
		set.contentLength = strconv.AppendInt(w.clenBuf[:0], int64(len(p)), 10)
	}
	hasCL := w.contentLength != -1
	if w.wantsClose {
		w.closeAfterReply = true
	}
	if headerGet(header, "Connection") == "close" {
		w.closeAfterReply = true
	}
	if w.bodyDeclared && !w.closeAfterReply && w.bodyFailed && w.bodyUnread >= maxPostHandlerReadBytes {
		// net/http's tooBig: it will not read the rest after the handler.
		w.bodyLimitHit = true
		delHeader("Connection")
		set.connection = "close"
	}
	code := w.status
	if bodyAllowedForStatus(code) {
		_, haveType := header["Content-Type"]
		ce := headerGet(header, "Content-Encoding")
		if ce == "" && !haveType && !hasTE && len(p) > 0 {
			set.contentType = http.DetectContentType(p)
		}
	} else {
		keys := []string{"Content-Length", "Transfer-Encoding"}
		if code == 304 {
			keys = []string{"Content-Type", "Content-Length", "Transfer-Encoding"}
		}
		for _, k := range keys {
			delHeader(k)
		}
	}
	if !headerHas(header, "Date") {
		set.date = time.Now().UTC().AppendFormat(w.dateBuf[:0], http.TimeFormat)
	}
	if hasCL && hasTE && te != "identity" {
		delHeader("Content-Length")
		hasCL = false
	}
	switch {
	case w.head || !bodyAllowedForStatus(code) || code == http.StatusNoContent:
		delHeader("Transfer-Encoding")
	case hasCL:
		delHeader("Transfer-Encoding")
	case hasTE && te == "identity":
		cw.chunking = false
		w.closeAfterReply = true
		delHeader("Transfer-Encoding")
	default:
		cw.chunking = true
		set.transferEncoding = "chunked"
		if hasTE && te == "chunked" {
			delHeader("Transfer-Encoding")
		}
	}
	if cw.chunking {
		delHeader("Content-Length")
	}
	if w.bodyLimitHit {
		w.closeAfterReply = true
	}
	if w.closeAfterReply && !hasToken(headerGet(cw.header, "Connection"), "close") && !isProtocolSwitchResponse(w.status, header) {
		delHeader("Connection")
		set.connection = "close"
	}
	bw := w.e.bufw
	w.writeStatusLine(code)
	_ = cw.header.WriteSubset(bw, excludeHeader)
	set.write(bw)
	_, _ = bw.WriteString("\r\n")
}

// extraHeader is net/http's: the header lines writeHeader adds, written
// in its order.
type extraHeader struct {
	contentType, connection, transferEncoding string
	date, contentLength                       []byte
}

func (h extraHeader) write(w *bufio.Writer) {
	if h.date != nil {
		_, _ = w.WriteString("Date: ")
		_, _ = w.Write(h.date)
		_, _ = w.WriteString("\r\n")
	}
	if h.contentLength != nil {
		_, _ = w.WriteString("Content-Length: ")
		_, _ = w.Write(h.contentLength)
		_, _ = w.WriteString("\r\n")
	}
	for i, v := range []string{h.contentType, h.connection, h.transferEncoding} {
		if v != "" {
			_, _ = w.WriteString([]string{"Content-Type", "Connection", "Transfer-Encoding"}[i])
			_, _ = w.WriteString(": ")
			_, _ = w.WriteString(v)
			_, _ = w.WriteString("\r\n")
		}
	}
}

func isProtocolSwitchResponse(code int, h http.Header) bool {
	return code == http.StatusSwitchingProtocols && h.Get("Upgrade") != "" && valuesContainToken(h["Connection"], "Upgrade")
}

// foreachHeaderElement is net/http's: fn for each non-empty element of
// the comma-separated v.
func foreachHeaderElement(v string, fn func(string)) {
	v = textproto.TrimString(v)
	if v == "" {
		return
	}
	if !strings.Contains(v, ",") {
		fn(v)
		return
	}
	for f := range strings.SplitSeq(v, ",") {
		if f = textproto.TrimString(f); f != "" {
			fn(f)
		}
	}
}
