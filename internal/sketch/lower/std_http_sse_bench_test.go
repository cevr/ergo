package lower

import "testing"

// BenchmarkStdSSE is the server-sent events reader's hand-Go control and
// floor (NG1 SSE; STD SPEED = GO FLOOR). The body is 1,000 events, about
// 64 KiB: an event line on every third, an id on each, one to three data
// lines of 40 bytes, LF line ends with CRLF on every fifth event, a
// comment on every seventh.
//
//   - SSEErgo: http.ReadEvents of the whole body.
//   - SSEErgoChunked: http.Feed over 4 KiB parts, as a stream gives them.
//   - SSEErgoView: http.FeedEach over the whole body, each view read
//     through DataLines and DataLine (SSE-VIEW): no event value, no joined
//     data, no slice of events; its floor is SSEFloor's.
//   - SSEGo: the hand-Go reader, the WHATWG rules over the string with
//     strings.IndexAny and strings.Cut, a []byte for the data, one
//     string per event's data (the shape of a bufio.Scanner loop with
//     the same field rules, without its copy into a buffer).
//   - SSEFloor: the least any reader does, a scan for each line end and
//     nothing else.
//
// Each Ergo bench first checks that its sum of data lengths equals hand
// Go's, so a wrong reader cannot be fast. Expected: SSEErgo within a
// small factor of SSEGo, with one string per event's data, as hand Go;
// SSEErgoChunked above it by the concatenation of a line split across
// parts. Reported either way.
func BenchmarkStdSSE(b *testing.B) {
	const src = `import http
import strings

func Whole(body string) int {
	let events = http.ReadEvents(body) catch {
	case http.Truncated:
		return -1
	}
	let mut n = 0
	for e in events {
		n = n + len(e.Data) + 1
	}
	n
}

func Chunked(body string, size int) int {
	let mut r = http.NewEventReader()
	let mut n = 0
	let mut at = 0
	for _ in 0..len(body)/size+1 {
		if at >= len(body) {
			break
		}
		let end = if at+size > len(body) { len(body) } else { at + size }
		let next, events = http.Feed(r, strings.Slice(body, at, end))
		r = next
		for e in events {
			n = n + len(e.Data) + 1
		}
		at = end
	}
	n
}

func View(body string) int {
	let mut r = http.NewEventReader()
	let mut n = 0
	http.FeedEach(&r, body, func(e mut http.EventView) {
		for i in 0..http.DataLines(e) {
			n = n + len(http.DataLine(e, i)) + 1
		}
	})
	n
}

func main() needs Console {
	Console.Println("\(Whole("data: a\n\n")) \(Chunked("data: a\n\n", 3)) \(View("data: a\n\n"))")
}
`
	gen := func(mainGo string) string {
		return `package main

import (
	"strconv"
	"strings"
	"testing"
)

var body = func() string {
	var b strings.Builder
	line := strings.Repeat("0123456789", 4)
	for i := range 1000 {
		end := "\n"
		if i%5 == 0 {
			end = "\r\n"
		}
		if i%7 == 0 {
			b.WriteString(": keep alive" + end)
		}
		if i%3 == 0 {
			b.WriteString("event: update" + end)
		}
		b.WriteString("id: " + strconv.Itoa(i) + end)
		for range 1 + i%3 {
			b.WriteString("data: " + line + end)
		}
		b.WriteString(end)
	}
	return b.String()
}()

var sink int

type event struct{ Event, Data, ID string }

// handRead is the hand-Go reader: the WHATWG rules, as std's reader.
func handRead(body string) []event {
	var out []event
	var kind, id string
	var data []byte
	has := false
	i := 0
	if strings.HasPrefix(body, "\ufeff") {
		i = 3
	}
	for i < len(body) {
		j := strings.IndexAny(body[i:], "\r\n")
		if j < 0 {
			break
		}
		line := body[i : i+j]
		k := i + j
		if body[k] == '\r' && k+1 < len(body) && body[k+1] == '\n' {
			i = k + 2
		} else {
			i = k + 1
		}
		if line == "" {
			if has {
				if kind == "" {
					kind = "message"
				}
				out = append(out, event{Event: kind, Data: string(data[:len(data)-1]), ID: id})
			}
			kind, data, has = "", data[:0], false
			continue
		}
		if line[0] == ':' {
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if found {
			value = strings.TrimPrefix(value, " ")
		}
		switch field {
		case "event":
			kind = value
		case "data":
			data = append(append(data, value...), '\n')
			has = true
		case "id":
			if strings.IndexByte(value, 0) < 0 {
				id = value
			}
		case "retry":
			_, _ = strconv.Atoi(value)
		}
	}
	return out
}

func handSum(body string) int {
	n := 0
	for _, e := range handRead(body) {
		n += len(e.Data) + 1
	}
	return n
}

// floor scans for each line end and does nothing else.
func floor(body string) int {
	n := 0
	for i := 0; i < len(body); {
		j := strings.IndexAny(body[i:], "\r\n")
		if j < 0 {
			break
		}
		n++
		i += j + 1
	}
	return n
}

func agree(b *testing.B, got int) {
	if want := handSum(body); got != want {
		b.Fatalf("Ergo %d, hand Go %d", got, want)
	}
	b.ResetTimer()
}

func BenchmarkSSEErgo(b *testing.B) {
	agree(b, ` + emittedCall(mainGo, "Whole", "body") + `)
	for range b.N {
		sink = ` + emittedCall(mainGo, "Whole", "body") + `
	}
}

func BenchmarkSSEErgoChunked(b *testing.B) {
	agree(b, ` + emittedCall(mainGo, "Chunked", "body, 4096") + `)
	for range b.N {
		sink = ` + emittedCall(mainGo, "Chunked", "body, 4096") + `
	}
}

func BenchmarkSSEErgoView(b *testing.B) {
	agree(b, ` + emittedCall(mainGo, "View", "body") + `)
	for range b.N {
		sink = ` + emittedCall(mainGo, "View", "body") + `
	}
}

func BenchmarkSSEGo(b *testing.B) {
	for range b.N {
		sink = handSum(body)
	}
}

func BenchmarkSSEFloor(b *testing.B) {
	for range b.N {
		sink = floor(body)
	}
}
`
	}
	emittedBench(b, "std_sse", src, gen, "SSEErgo", "SSEErgoChunked", "SSEGo", "SSEFloor")
}
