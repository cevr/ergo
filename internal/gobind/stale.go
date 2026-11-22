package gobind

import (
	"sort"
	"strconv"
	"strings"
)

// Stale is how a committed set of bindings, by import path, differs from
// the set ergo bind writes now: one line per path, in path order, for a
// binding that differs (with its first differing line), one the set has
// and the writer no longer writes, and one the writer writes and the set
// lacks. It is empty when the two sets are equal.
func Stale(committed, written map[string]string) []string {
	paths := map[string]bool{}
	for p := range committed {
		paths[p] = true
	}
	for p := range written {
		paths[p] = true
	}
	order := make([]string, 0, len(paths))
	for p := range paths {
		order = append(order, p)
	}
	sort.Strings(order)
	var out []string
	for _, p := range order {
		c, inCommitted := committed[p]
		w, inWritten := written[p]
		switch {
		case !inWritten:
			out = append(out, p+": committed, but ergo bind go no longer writes it")
		case !inCommitted:
			out = append(out, p+": ergo bind go writes it, but it is not committed")
		case c != w:
			out = append(out, p+": differs from what ergo bind go writes, "+firstDiff(c, w))
		}
	}
	return out
}

// firstDiff is the first line where a and b differ, both sides.
func firstDiff(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := range max(len(al), len(bl)) {
		var x, y string
		if i < len(al) {
			x = al[i]
		}
		if i < len(bl) {
			y = bl[i]
		}
		if x != y {
			return "line " + strconv.Itoa(i+1) + ": committed " + strconv.Quote(x) + ", written " + strconv.Quote(y)
		}
	}
	return "no line differs"
}
