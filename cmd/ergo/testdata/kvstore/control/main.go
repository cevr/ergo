// Command control is the hand-written Go control of examples/kvstore's
// session on the memory store (wf10 GC-EDGE): one request per line of
// standard input, answered from a map, as a Go programmer would write it.
// -pace chooses the same pacing as the Ergo program's flag, by the same
// public Go calls: default (Go's own pace), percent (GOGC=400) or edges
// (GOGC off, a 256 MiB memory limit, and runtime.GC between requests once
// the heap has grown by a quarter of the limit, read by std pace's
// AtEdges rule). The GC-EDGE receipt counts it beside the Ergo program.
package main

import (
	"bufio"
	"flag"
	"io"
	"os"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
)

const paceLimit = 256 << 20

// atEdges is std pace's AtEdges in Go: an edge counts down to the next
// read of the heap; a read collects once the heap has grown by grow since
// the last collection, and else sets the next read.
type atEdges struct {
	grow, base, last, span, wait int64
	stats                        runtime.MemStats
}

func newAtEdges(limit int64) *atEdges {
	debug.SetGCPercent(-1)
	debug.SetMemoryLimit(limit)
	p := &atEdges{grow: limit / 4, span: 1, wait: 1}
	runtime.ReadMemStats(&p.stats)
	p.base, p.last = int64(p.stats.HeapAlloc), int64(p.stats.HeapAlloc)
	return p
}

func (p *atEdges) edge() {
	if p.wait--; p.wait != 0 {
		return
	}
	runtime.ReadMemStats(&p.stats)
	heap := int64(p.stats.HeapAlloc)
	grown := heap - p.base
	if grown >= p.grow {
		runtime.GC()
		runtime.ReadMemStats(&p.stats)
		p.base, p.last, p.span, p.wait = int64(p.stats.HeapAlloc), int64(p.stats.HeapAlloc), 1, 1
		return
	}
	next := nextRead(heap-p.last, p.span, p.grow-grown)
	p.last, p.span, p.wait = heap, next, next
}

func nextRead(grown, edges, room int64) int64 {
	if room <= 0 {
		return 1
	}
	if grown <= 0 {
		return spanOf(edges * 2)
	}
	per := grown
	if edges > 1 {
		per = grown / edges
	}
	return spanOf(room / max(per, 1) / 2)
}

func spanOf(n int64) int64 { return min(max(n, 1), 256) }

func main() {
	pace := flag.String("pace", "default", "default, percent or edges")
	flag.Parse()
	var edge func()
	switch *pace {
	case "percent":
		debug.SetGCPercent(400)
	case "edges":
		edge = newAtEdges(paceLimit).edge
	}
	in, err := io.ReadAll(bufio.NewReader(os.Stdin))
	if err != nil {
		os.Exit(1)
	}
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	store := map[string]string{}
	text := string(in)
	for text != "" {
		line := text
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			line, text = text[:i], text[i+1:]
		} else {
			text = ""
		}
		answer(out, store, line)
		if edge != nil {
			edge()
		}
	}
}

func split(text string) (string, string) {
	before, after, found := strings.Cut(text, " ")
	if !found {
		return text, ""
	}
	return before, strings.TrimSpace(after)
}

func sortedKeys(store map[string]string) []string {
	keys := make([]string, 0, len(store))
	for k := range store {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func answer(out *bufio.Writer, store map[string]string, line string) {
	word, rest := split(strings.TrimSpace(line))
	usage := func(want string) { out.WriteString("? usage: " + want + "\n") }
	one := func() (string, bool) {
		if rest == "" || strings.Contains(rest, " ") {
			usage(word + " KEY")
			return "", false
		}
		return rest, true
	}
	switch word {
	case "":
	case "put":
		key, value := split(rest)
		if key == "" {
			usage("put KEY VALUE")
			return
		}
		store[key] = value
		out.WriteString("ok\n")
	case "get":
		key, ok := one()
		if !ok {
			return
		}
		if v, found := store[key]; found {
			out.WriteString(v + "\n")
		} else {
			out.WriteString("missing " + key + "\n")
		}
	case "del":
		key, ok := one()
		if !ok {
			return
		}
		if _, found := store[key]; !found {
			out.WriteString("missing " + key + "\n")
			return
		}
		delete(store, key)
		out.WriteString("deleted " + key + "\n")
	case "keys", "dump", "compact":
		if rest != "" {
			usage(word)
			return
		}
		switch word {
		case "keys":
			keys := sortedKeys(store)
			if len(keys) == 0 {
				out.WriteString("no keys\n")
			}
			for _, k := range keys {
				out.WriteString(k + "\n")
			}
		case "dump":
			for _, k := range sortedKeys(store) {
				out.WriteString(k + "=" + strconv.Quote(store[k]) + "\n")
			}
		default:
			out.WriteString("compacted\n")
		}
	default:
		out.WriteString("? unknown command " + word + "; commands: put get del keys dump compact\n")
	}
}
