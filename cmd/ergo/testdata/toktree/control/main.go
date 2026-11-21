// Command control is the hand Go control of examples/toktree (wf10
// self-layout plan §2.3): it reads a file of source text, scans it
// into two token columns (kind, start), builds a flat tree of bracket
// groups (rows in child-before-parent order, child lists in one pool, one
// scratch stack with marks), and prints the line the Ergo program prints.
//
//	control FILE       columns sized from the input; child lists in one pool
//	control -nocap FILE  no capacity given (what std dense can say before SL2)
//	control -sub FILE  no child lists: a group holds the row count of its
//	                   subtree (Carbon's form); its nodesum differs
package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

const (
	tComment = 1
	tIdent   = 2
	tNumber  = 3
	tString  = 4
	tRune    = 5
	tOpen    = 6
	tClose   = 7
	tPunct   = 8
)

const (
	nLeaf  = 1
	nGroup = 2
	nRoot  = 3
)

const none = ^uint32(0)

type toks struct {
	kind  []uint8
	start []uint32
}

func isIdentStart(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_'
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func scan(src string, capacity int) toks {
	t := toks{kind: make([]uint8, 0, capacity), start: make([]uint32, 0, capacity)}
	i := 0
	n := len(src)
	for i < n {
		c := src[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			i++
			continue
		}
		start := i
		kind := uint8(tPunct)
		switch {
		case isIdentStart(c):
			kind = tIdent
			i++
			for i < n && (isIdentStart(src[i]) || isDigit(src[i])) {
				i++
			}
		case isDigit(c):
			kind = tNumber
			i++
			for i < n && (isIdentStart(src[i]) || isDigit(src[i]) || src[i] == '.') {
				i++
			}
		case c == '/' && i+1 < n && src[i+1] == '/':
			kind = tComment
			i += 2
			for i < n && src[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && src[i+1] == '*':
			kind = tComment
			i += 2
			for i < n && !(src[i] == '*' && i+1 < n && src[i+1] == '/') {
				i++
			}
			if i < n {
				i += 2
			}
		case c == '"' || c == '\'':
			kind = tString
			if c == '\'' {
				kind = tRune
			}
			i++
			for i < n && src[i] != c && src[i] != '\n' {
				if src[i] == '\\' && i+1 < n {
					i++
				}
				i++
			}
			if i < n && src[i] == c {
				i++
			}
		case c == '`':
			kind = tString
			i++
			for i < n && src[i] != '`' {
				i++
			}
			if i < n {
				i++
			}
		case c == '(' || c == '[' || c == '{':
			kind = tOpen
			i++
		case c == ')' || c == ']' || c == '}':
			kind = tClose
			i++
		default:
			i++
		}
		t.kind = append(t.kind, kind)
		t.start = append(t.start, uint32(start))
	}
	return t
}

type tree struct {
	kind []uint8
	tok  []uint32
	a, b []uint32
	pool []uint32
}

func (tr *tree) add(kind uint8, tok, a, b uint32) uint32 {
	id := uint32(len(tr.kind))
	tr.kind = append(tr.kind, kind)
	tr.tok = append(tr.tok, tok)
	tr.a = append(tr.a, a)
	tr.b = append(tr.b, b)
	return id
}

// build makes the tree with child lists in the pool. depth is the deepest nesting.
func build(t toks, capacity int) (tree, int) {
	tr := tree{kind: make([]uint8, 0, capacity), tok: make([]uint32, 0, capacity), a: make([]uint32, 0, capacity), b: make([]uint32, 0, capacity), pool: make([]uint32, 0, capacity)}
	stack := []uint32{}
	marks := []int{}
	opens := []uint32{}
	depth := 0
	closeGroup := func() {
		mark := marks[len(marks)-1]
		marks = marks[:len(marks)-1]
		open := opens[len(opens)-1]
		opens = opens[:len(opens)-1]
		start := uint32(len(tr.pool))
		tr.pool = append(tr.pool, stack[mark:]...)
		id := tr.add(nGroup, open, start, uint32(len(stack)-mark))
		stack = append(stack[:mark], id)
	}
	for i, k := range t.kind {
		switch k {
		case tComment:
		case tOpen:
			marks = append(marks, len(stack))
			opens = append(opens, uint32(i))
			if len(marks) > depth {
				depth = len(marks)
			}
		case tClose:
			if len(marks) == 0 {
				stack = append(stack, tr.add(nLeaf, uint32(i), none, none))
			} else {
				closeGroup()
			}
		default:
			stack = append(stack, tr.add(nLeaf, uint32(i), none, none))
		}
	}
	for len(marks) > 0 {
		closeGroup()
	}
	start := uint32(len(tr.pool))
	tr.pool = append(tr.pool, stack...)
	tr.add(nRoot, 0, start, uint32(len(stack)))
	return tr, depth
}

// buildSub makes the tree with no child lists: a group's a is the number of
// rows in its subtree, itself included.
func buildSub(t toks, capacity int) (tree, int) {
	tr := tree{kind: make([]uint8, 0, capacity), tok: make([]uint32, 0, capacity), a: make([]uint32, 0, capacity)}
	rows := []uint32{}
	opens := []uint32{}
	depth := 0
	add := func(kind uint8, tok, a uint32) {
		tr.kind = append(tr.kind, kind)
		tr.tok = append(tr.tok, tok)
		tr.a = append(tr.a, a)
	}
	closeGroup := func() {
		row := rows[len(rows)-1]
		rows = rows[:len(rows)-1]
		open := opens[len(opens)-1]
		opens = opens[:len(opens)-1]
		add(nGroup, open, uint32(len(tr.kind))-row+1)
	}
	for i, k := range t.kind {
		switch k {
		case tComment:
		case tOpen:
			rows = append(rows, uint32(len(tr.kind)))
			opens = append(opens, uint32(i))
			if len(rows) > depth {
				depth = len(rows)
			}
		case tClose:
			if len(rows) == 0 {
				add(nLeaf, uint32(i), 1)
			} else {
				closeGroup()
			}
		default:
			add(nLeaf, uint32(i), 1)
		}
	}
	for len(rows) > 0 {
		closeGroup()
	}
	add(nRoot, 0, uint32(len(tr.kind))+1)
	return tr, depth
}

func main() {
	nocap, sub := false, false
	var files []string
	for _, a := range os.Args[1:] {
		switch a {
		case "-nocap":
			nocap = true
		case "-sub":
			sub = true
		default:
			files = append(files, a)
		}
	}
	if len(files) != 1 {
		fmt.Fprintln(os.Stderr, "usage: toktree FILE")
		os.Exit(2)
	}
	data, err := os.ReadFile(files[0])
	switch {
	case errors.Is(err, fs.ErrNotExist):
		fmt.Fprintln(os.Stderr, "toktree: no file "+files[0])
		os.Exit(1)
	case errors.Is(err, fs.ErrPermission):
		fmt.Fprintln(os.Stderr, "toktree: cannot read "+files[0]+": permission denied")
		os.Exit(1)
	case err != nil:
		fmt.Fprintln(os.Stderr, "toktree: cannot read "+files[0]+": "+err.Error())
		os.Exit(1)
	}
	src := string(data)
	capacity := len(src) / 4
	if nocap {
		capacity = 0
	}
	t := scan(src, capacity)
	tokSum := uint32(0)
	for i, k := range t.kind {
		tokSum = tokSum*31 + uint32(k) + t.start[i]
	}
	var tr tree
	depth := 0
	nodeSum := uint32(0)
	groups := 0
	if sub {
		tr, depth = buildSub(t, capacity)
		for i, k := range tr.kind {
			if k != nLeaf {
				groups++
				// The children of row i: walk back over its subtree, one child subtree at a time.
				end := uint32(i) - (tr.a[i] - 1)
				for c := uint32(i); c > end; {
					c--
					nodeSum = nodeSum*31 + uint32(tr.kind[c])
					c -= tr.a[c] - 1
				}
			}
		}
	} else {
		tr, depth = build(t, capacity)
		for i, k := range tr.kind {
			if k != nLeaf {
				groups++
				for _, c := range tr.pool[tr.a[i] : tr.a[i]+tr.b[i]] {
					nodeSum = nodeSum*31 + uint32(tr.kind[c])
				}
			}
		}
	}
	fmt.Printf("tokens=%d nodes=%d groups=%d depth=%d pool=%d toksum=%d nodesum=%d\n", len(t.kind), len(tr.kind), groups, depth, len(tr.pool), tokSum, nodeSum)
}
