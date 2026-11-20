// Command control is the hand Go control of examples/tn1-lint's workload
// (scripts/opcount -control): the TypeScript host of TN1's record
// (ergo.dev/tn1/ts) and the record's text form, written as plain Go with
// the same algorithm (byte switches, maps for the word sets, one scan,
// preallocated tokens, each run of three tokens hashed once). It prints
// the record of FILE, byte for byte what `tn1-lint record FILE` prints;
// opcount refuses the comparison when the two differ.
//
//	control record FILE
package main

import (
	"bufio"
	"fmt"
	"math/bits"
	"os"
	"strconv"
	"strings"
)

type kind uint8

const (
	ident kind = iota
	punct
	literal
	lineComment
	blockComment
)

type token struct {
	kind     kind
	from, to int
	text     string
	newLine  bool
	depth    int
}

func (t *token) isComment() bool { return t.kind == lineComment || t.kind == blockComment }
func (t *token) isIdent() bool   { return t.kind == ident }
func (t *token) word() bool      { return t.kind == ident || t.kind == punct }

func identStart(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || c == '$' || c >= 0x80
}
func identByte(c byte) bool { return identStart(c) || c >= '0' && c <= '9' }
func digit(c byte) bool     { return c >= '0' && c <= '9' }

// A byte's class, which the scanner switches on: cIdent and cDigit come
// first, so an identifier byte is a class of at most cDigit.
const (
	cIdent uint8 = iota
	cDigit
	cSpace
	cNewline
	cSlash
	cQuote
	cTick
	cClose
	cDot
	cHash
	cBackslash
	cOther
)

var classes = func() (t [256]uint8) {
	for c := range t {
		switch {
		case identStart(byte(c)):
			t[c] = cIdent
		case digit(byte(c)):
			t[c] = cDigit
		default:
			t[c] = cOther
		}
	}
	for _, c := range []byte(" \t\r\f\v") {
		t[c] = cSpace
	}
	t['\n'], t['/'], t['"'], t['\''], t['`'] = cNewline, cSlash, cQuote, cQuote, cTick
	t['}'], t['.'], t['#'], t['\\'] = cClose, cDot, cHash, cBackslash
	return t
}()

// punctLen is the length of the punctuator at src[i], the longest of
// "...", "===", "!==", "**=", "<<=", "&&=", "||=", "??=", "=>", "==",
// "!=", "<=", "&&", "||", "??", "?.", "++", "--", "+=", "-=", "*=", "/=",
// "%=", "&=", "|=", "^=", "**", "<<" that starts there, else 1. A > is
// always one token, as TypeScript's scanner reads it.
func punctLen(src string, i int) int {
	at := func(k int) byte {
		if i+k < len(src) {
			return src[i+k]
		}
		return 0
	}
	c, d := src[i], at(1)
	switch c {
	case '.':
		if d == '.' && at(2) == '.' {
			return 3
		}
	case '=':
		if d == '=' && at(2) == '=' {
			return 3
		}
		if d == '>' || d == '=' {
			return 2
		}
	case '!':
		if d == '=' {
			if at(2) == '=' {
				return 3
			}
			return 2
		}
	case '*', '<', '&', '|', '?':
		if d == c {
			if at(2) == '=' {
				return 3
			}
			return 2
		}
		if d == '=' && c != '?' || c == '?' && d == '.' {
			return 2
		}
	case '+', '-':
		if d == c || d == '=' {
			return 2
		}
	case '/', '%', '^':
		if d == '=' {
			return 2
		}
	}
	return 1
}

func set(xs ...string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

var regexAfter = set("return", "typeof", "instanceof", "in", "of", "new", "delete", "void", "throw", "case", "do", "else", "yield", "await")

func regexAllowed(toks []token) bool {
	if len(toks) == 0 {
		return true
	}
	p := &toks[len(toks)-1]
	switch p.kind {
	case punct:
		return p.text != ")" && p.text != "]"
	case ident:
		return regexAfter[p.text]
	}
	return false
}

func quoted(src string, i int) int {
	q := src[i]
	j := i + 1
	for j < len(src) && src[j] != q && src[j] != '\n' {
		if src[j] == '\\' {
			j++
		}
		j++
	}
	if j < len(src) && src[j] == q {
		return j + 1
	}
	return min(j, len(src))
}

func templateEnd(src string, i int) (int, bool) {
	j := i
	for j < len(src) {
		c := src[j]
		switch {
		case c == '\\':
			j += 2
		case c == '`':
			return j + 1, false
		case c == '$' && j+1 < len(src) && src[j+1] == '{':
			return j + 2, true
		default:
			j++
		}
	}
	return len(src), false
}

func regexEnd(src string, i int) int {
	j := i + 1
	class := false
	for j < len(src) && src[j] != '\n' {
		c := src[j]
		if c == '\\' {
			j += 2
			continue
		}
		if c == '[' {
			class = true
		} else if c == ']' {
			class = false
		} else if c == '/' && !class {
			j++
			for j < len(src) && identByte(src[j]) {
				j++
			}
			return j
		}
		j++
	}
	return min(j, len(src))
}

func scan(src string) []token {
	toks := make([]token, 0, len(src)/4)
	var stack []bool
	i := 0
	newLine := false
	if strings.HasPrefix(src, "#!") {
		end := strings.IndexByte(src, '\n')
		to := end
		if end < 0 {
			to = len(src)
		}
		toks = append(toks, token{kind: lineComment, to: to})
		i = to
	}
	for i < len(src) {
		c := src[i]
		cl := classes[c]
		if cl == cNewline {
			newLine = true
			i++
			continue
		}
		if cl == cSpace {
			i++
			continue
		}
		depth := len(stack)
		k := punct
		end := i + 1
		text := ""
		next := byte(0)
		if i+1 < len(src) {
			next = src[i+1]
		}
		switch {
		case cl == cSlash && next == '/':
			k = lineComment
			n := strings.IndexByte(src[i:], '\n')
			end = len(src)
			if n >= 0 {
				end = i + n
			}
		case cl == cSlash && next == '*':
			k = blockComment
			n := strings.Index(src[i+2:], "*/")
			end = len(src)
			if n >= 0 {
				end = i + 2 + n + 2
			}
		case cl == cQuote:
			k = literal
			end = quoted(src, i)
		case cl == cTick:
			k = literal
			var opens bool
			end, opens = templateEnd(src, i+1)
			if opens {
				stack = append(stack, true)
			}
		case cl == cClose && depth > 0 && stack[depth-1]:
			k = literal
			stack = stack[:depth-1]
			var opens bool
			end, opens = templateEnd(src, i+1)
			if opens {
				stack = append(stack, true)
			}
		case cl == cDigit || cl == cDot && classes[next] == cDigit:
			k = literal
			hex := c == '0' && (next == 'x' || next == 'X')
			j := i + 1
			for j < len(src) && (classes[src[j]] <= cDigit || src[j] == '.' || (src[j] == '+' || src[j] == '-') && (src[j-1] == 'e' || src[j-1] == 'E') && !hex) {
				j++
			}
			end = j
		case cl == cIdent || cl == cHash && classes[next] == cIdent || cl == cBackslash:
			k = ident
			j := i + 1
			for j < len(src) && (classes[src[j]] <= cDigit || src[j] == '\\') {
				j++
			}
			end = j
			text = src[i:end]
		case cl == cSlash && regexAllowed(toks):
			k = literal
			end = regexEnd(src, i)
		default:
			end = i + punctLen(src, i)
			text = src[i:end]
			if c == '{' {
				stack = append(stack, false)
			} else if c == '}' && depth > 0 {
				stack = stack[:depth-1]
			}
		}
		toks = append(toks, token{kind: k, from: i, to: end, text: text, newLine: newLine, depth: depth})
		newLine = false
		i = end
	}
	return toks
}

func codeOf(src string, toks []token) []token {
	out := make([]token, 0, len(toks))
	pending := false
	for _, t := range toks {
		if t.isComment() {
			pending = pending || t.newLine || strings.IndexByte(src[t.from:t.to], '\n') >= 0 || t.kind == lineComment
			continue
		}
		t.newLine = t.newLine || pending
		pending = false
		out = append(out, t)
	}
	return out
}

// Statements.

var noEnd = set("=", "=>", ",", ".", "?.", "(", "[", "{", "?", ":", "+", "-", "*", "/", "%", "**", "&", "|", "^", "&&", "||", "??",
	"==", "!=", "===", "!==", "<=", ">=", "<<", ">>", ">>>", "+=", "-=", "*=", "/=", "%=", "**=", "<<=", ">>=", ">>>=", "&=", "|=", "^=", "&&=", "||=", "??=",
	"...", "@",
	"export", "default", "declare", "abstract", "async", "import", "const", "let", "var", "class", "function", "new", "extends", "implements",
	"interface", "enum", "namespace", "module", "as", "satisfies", "keyof", "typeof", "in", "instanceof", "is", "readonly", "unique", "infer")

var continues = set(".", "?.", ",", ")", "]", "}", "=>", "?", ":", "=", "(", "[", "+", "-", "*", "/", "%", "**", "&", "|", "^", "&&", "||", "??",
	"==", "!=", "===", "!==", "<=", ">=", "<<", ">>", ">>>", "+=", "-=", "*=", "/=", "%=", "**=", "<<=", ">>=", ">>>=", "&=", "|=", "^=", "&&=", "||=", "??=",
	"as", "satisfies", "extends", "implements", "instanceof", "in", "else", "catch", "finally")

var blocks = set("function", "class", "interface", "enum", "namespace", "module", "global", "if", "for", "while", "switch", "try", "do")
var modifiers = set("export", "default", "declare", "abstract", "async")
var blockNext = set("else", "catch", "finally", "while")

func head(code []token, from, to int) string {
	i := from
	for i < to {
		t := &code[i]
		if t.text == "@" {
			i++
			for i < to && (code[i].text == "." || code[i].isIdent() && i > from && (code[i-1].text == "@" || code[i-1].text == ".")) {
				i++
			}
			if i < to && code[i].text == "(" {
				i = closing(code, i, to) + 1
			}
			continue
		}
		if t.isIdent() && modifiers[t.text] {
			i++
			continue
		}
		if t.isIdent() && i+1 < to && code[i+1].text == ":" && code[i+1].word() {
			i += 2
			continue
		}
		return t.text
	}
	return ""
}

func closing(code []token, i, to int) int {
	n := 0
	for j := i; j < to; j++ {
		switch code[j].text {
		case "(", "[", "{":
			n++
		case ")", "]", "}":
			n--
			if n == 0 {
				return j
			}
		}
	}
	return to - 1
}

type span struct{ from, to int }

func seenBrace(code []token, from, to int) bool {
	for j := from; j < to; j++ {
		switch code[j].text {
		case "{", "class", "function":
			return true
		}
	}
	return false
}

func statements(code []token) []span {
	var out []span
	start := -1
	nest := 0
	for i := range code {
		t := &code[i]
		if start >= 0 && nest == 0 && t.newLine {
			p := &code[i-1]
			decorated := code[start].text == "@" && !seenBrace(code, start, i)
			if !decorated && !(p.word() && noEnd[p.text]) && !(t.word() && continues[t.text]) {
				out = append(out, span{code[start].from, p.to})
				start = -1
			}
		}
		if start < 0 {
			start = i
		}
		x := ""
		if t.kind == punct {
			x = t.text
		}
		switch x {
		case "(", "[", "{":
			nest++
		case ")", "]", "}":
			if nest > 0 {
				nest--
			}
		}
		if nest == 0 && x == ";" {
			out = append(out, span{code[start].from, t.to})
			start = -1
		} else if nest == 0 && x == "}" {
			h := head(code, start, i+1)
			if blocks[h] {
				next := i + 1
				if next >= len(code) || !(code[next].word() && blockNext[code[next].text]) {
					if h != "do" {
						out = append(out, span{code[start].from, t.to})
						start = -1
					}
				}
			}
		}
	}
	if start >= 0 {
		out = append(out, span{code[start].from, code[len(code)-1].to})
	}
	return out
}

// Words and sketches.

func letter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// wordsSketch is the sketch of the words of s (tn1.Words then tn1.Sketch).
func wordsSketch(s string, sk uint64) uint64 {
	start := -1
	for i := 0; i <= len(s); i++ {
		end := i == len(s) || !letter(s[i]) || start >= 0 && s[i] <= 'Z' && s[i-1] >= 'a'
		if end && start >= 0 {
			if i-start >= 3 {
				h := uint32(2166136261)
				for k := start; k < i; k++ {
					c := s[k]
					if c >= 'A' && c <= 'Z' {
						c += 32
					}
					h = (h ^ uint32(c)) * 16777619
				}
				sk |= 1 << (h % 64)
			}
			start = -1
		}
		if i < len(s) && letter(s[i]) && start < 0 {
			start = i
		}
	}
	return sk
}

func wordBit(w string) uint64 {
	h := uint32(2166136261)
	for i := 0; i < len(w); i++ {
		h = (h ^ uint32(w[i])) * 16777619
	}
	return 1 << (h % 64)
}

// Record.

// directives are the openings of a comment body that make it a tool's,
// as in modules/tn1/ts/record.ergo.
var directives = []string{
	"oxlint-disable", "oxlint-enable", "eslint-disable", "eslint-enable", "eslint ",
	"@effect-diagnostics", "@ts-expect-error", "@ts-ignore", "@ts-nocheck", "@ts-check",
	"/ <reference", "/<reference", "/ <amd-module", "/ <amd-dependency",
	"#__PURE__", "@__PURE__", "#__NO_SIDE_EFFECTS__", "@__NO_SIDE_EFFECTS__", "#__KEY__", "@__KEY__",
	"#__INLINE__", "@__INLINE__", "#__NOINLINE__", "@__NOINLINE__", "@vite-ignore", "@jsx",
	"prettier-ignore", "oxfmt-ignore", "c8 ignore", "v8 ignore", "istanbul ignore",
	"!", "@license", "SPDX-License-Identifier:", "nosemgrep", "# sourceMappingURL=", "@ sourceMappingURL=", "#sourceMappingURL=", "@sourceMappingURL=",
	"# sourceURL=", "@ sourceURL=", "#sourceURL=", "@sourceURL=", "webpack", "turbopackIgnore",
	"biome-ignore", "ergo-lint-", "traits-ignore",
}
var controls = set("if", "for", "while", "switch", "catch", "function", "return", "typeof", "void", "delete", "await", "yield", "in", "of", "instanceof", "as", "satisfies", "keyof")
var assigns = set("=", "+=", "-=", "*=", "/=", "%=", "**=", "<<=", ">>=", ">>>=", "&=", "|=", "^=", "&&=", "||=", "??=")
var neutral = []string{"function", "type", "branch", "loop", "handler", "return", "", "", "test"}

func directive(text string) bool {
	if strings.HasPrefix(text, "#!") {
		return true
	}
	body := strings.TrimLeft(text[min(2, len(text)):], "*\n\r\t ")
	for _, d := range directives {
		if strings.HasPrefix(body, d) {
			return true
		}
	}
	return false
}

func lineStarts(src string) []int {
	if len(src) == 0 {
		return nil
	}
	out := []int{0}
	for i := 0; i < len(src); i++ {
		if src[i] == '\n' && i+1 < len(src) {
			out = append(out, i+1)
		}
	}
	return out
}

// lineFrom is lineOf(starts, p) for a p on line l or after it: a walk
// forward from l, which costs the lines passed, so a pass over tokens in
// order costs the file's lines once.
func lineFrom(starts []int, l, p int) int {
	for l+1 < len(starts) && starts[l+1] <= p {
		l++
	}
	return l
}

func lineOf(starts []int, p int) int {
	lo, hi := 0, len(starts)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if starts[mid] <= p {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
}

func imports(code []token) (map[string]bool, []bool) {
	names := map[string]bool{}
	clause := make([]bool, len(code))
	for i := 0; i < len(code); i++ {
		if code[i].text == "import" && code[i].depth == 0 && code[i].isIdent() && (i == 0 || code[i-1].text != ".") {
			j := i + 1
			for j < len(code) && code[j].text != "from" && code[j].text != ";" && code[j].text != "=" && !(j > i+1 && code[j].newLine && !code[j-1].word()) {
				if code[j].isIdent() {
					clause[j] = true
					after := ""
					if j+1 < len(code) {
						after = code[j+1].text
					}
					if code[j].text != "type" && code[j].text != "as" && after != "as" {
						names[code[j].text] = true
					}
				}
				j++
			}
			if j < len(code) && code[j].text == "=" && j > i+1 {
				names[code[j-1].text] = true
			}
			i = j
		}
	}
	return names, clause
}

func signature(code []token, open int) bool {
	c := closing(code, open, len(code))
	return c+1 < len(code) && (code[c+1].text == "{" || code[c+1].text == ":") && code[c+1].word()
}

func attached(toks []token, i int) bool {
	for j := i + 1; j < len(toks); j++ {
		if !toks[j].isComment() {
			x := toks[j].text
			return !(toks[j].word() && (x == ")" || x == "]" || x == "}"))
		}
	}
	return false
}

func trim(s string) string {
	a, b := 0, len(s)
	for a < b && (s[a] == ' ' || s[a] == '\t') {
		a++
	}
	for b > a && (s[b-1] == ' ' || s[b-1] == '\t') {
		b--
	}
	return s[a:b]
}

func hex16(w *bufio.Writer, v uint64) {
	const digits = "0123456789abcdef"
	var b [16]byte
	for i := 15; i >= 0; i-- {
		b[i] = digits[v&15]
		v >>= 4
	}
	w.Write(b[:])
}

var lineKinds = []string{"blank", "code", "comment", "doc", "directive"}

const (
	kBlank = iota
	kCode
	kComment
	kDoc
	kDirective
)

func record(w *bufio.Writer, src string) {
	starts := lineStarts(src)
	n := len(starts)
	toks := scan(src)
	code := codeOf(src, toks)
	names, clause := imports(code)

	mark := []byte(strings.Repeat("c", len(src)))
	for i := range toks {
		t := &toks[i]
		var m byte
		switch t.kind {
		case lineComment, blockComment:
			m = '/'
		case literal:
			m = '"'
		default:
			continue
		}
		for p := t.from; p < t.to; p++ {
			mark[p] = m
		}
	}
	for j := range code {
		t := &code[j]
		if t.isIdent() && (clause[j] || names[t.text]) {
			for p := t.from; p < t.to; p++ {
				mark[p] = '"'
			}
		}
	}

	doc := make([]bool, n)
	dir := make([]bool, n)
	depth := make([]int, n)
	deep := make([]bool, n)
	startBits := make([]int, n)
	at := 0 // tokens come in order, so their lines are found by walking forward
	for ti := range toks {
		t := &toks[ti]
		first := lineFrom(starts, at, t.from)
		at = first
		e := min(t.to, len(src))
		if t.to > t.from {
			e--
		}
		last := lineFrom(starts, first, e)
		for l := first; l <= last; l++ {
			if !deep[l] {
				deep[l] = true
				depth[l] = t.depth
			}
		}
		if t.isComment() {
			text := src[t.from:t.to]
			isDoc := strings.HasPrefix(text, "/**") && text != "/**/" && attached(toks, ti)
			isDir := directive(text)
			for l := first; l <= last; l++ {
				doc[l] = doc[l] || isDoc
				dir[l] = dir[l] || isDir
			}
		}
	}

	at = 0
	for j := range code {
		t := &code[j]
		next, prev := "", ""
		if j+1 < len(code) {
			next = code[j+1].text
		}
		if j > 0 {
			prev = code[j-1].text
		}
		w := t.text
		bits := 0
		if t.isIdent() {
			switch {
			case w == "function":
				bits = 1
			case w == "class" || w == "interface" || w == "enum" || w == "type" && j+1 < len(code) && code[j+1].isIdent() && prev != ".":
				bits = 2
			case w == "if" || w == "switch":
				bits = 4
			case w == "for" || w == "while" || w == "do":
				bits = 8
			case w == "return":
				bits = 32
			case w == "const" || w == "let" || w == "var":
				bits = 128
			}
			if w == "catch" || strings.HasPrefix(w, "catch") && len(w) > 5 && w[5] >= 'A' && w[5] <= 'Z' && next == "(" {
				bits |= 16
			}
			if next == "(" && !controls[w] && prev != "function" && !signature(code, j+1) {
				bits |= 64
				if (w == "test" || w == "it" || w == "describe") && prev != "." {
					bits |= 256
				}
			}
		} else if w == "=>" {
			bits = 1
		} else if assigns[w] {
			bits = 128
		}
		if bits != 0 {
			at = lineFrom(starts, at, t.from)
			startBits[at] |= bits
		}
	}

	w.WriteString("tn1-record 2 ts\n")
	comment := make([]byte, 0, 256)
	codeText := make([]byte, 0, 256)
	for l := 0; l < n; l++ {
		from := starts[l]
		to := len(src)
		if l+1 < n {
			to = starts[l+1] - 1
		} else if strings.HasSuffix(src, "\n") {
			to = len(src) - 1
		}
		comment = comment[:0]
		codeText = codeText[:0]
		hasCode := false
		for p := from; p < to; p++ {
			c := src[p]
			if mark[p] == '/' {
				if c != '\r' {
					comment = append(comment, c)
				}
				codeText = append(codeText, ' ')
			} else {
				if c != ' ' && c != '\t' && c != '\r' {
					hasCode = true
				}
				if mark[p] == 'c' {
					codeText = append(codeText, c)
				} else {
					codeText = append(codeText, ' ')
				}
			}
		}
		text := trim(string(comment))
		k := kComment
		switch {
		case hasCode:
			k = kCode
		case len(comment) == 0:
			k = kBlank
		case doc[l]:
			k = kDoc
		case dir[l]:
			k = kDirective
		}
		cw := wordsSketch(string(codeText), 0)
		for b := 0; b < 9; b++ {
			if (startBits[l]>>b)&1 == 1 && neutral[b] != "" {
				cw |= wordBit(neutral[b])
			}
		}
		w.WriteString("L ")
		w.WriteString(lineKinds[k])
		w.WriteByte(' ')
		w.WriteString(strconv.Itoa(depth[l]))
		w.WriteByte(' ')
		w.WriteString(strconv.FormatUint(uint64(startBits[l]), 16))
		w.WriteByte(' ')
		hex16(w, wordsSketch(text, 0))
		w.WriteByte(' ')
		hex16(w, cw)
		w.WriteByte(' ')
		w.WriteString(text)
		w.WriteByte('\n')
	}
	writeNodes(w, src, starts, code, names)
}

// Nodes.

var declarers = set("class", "interface", "enum", "type", "namespace", "module", "declare", "global")
var keywords = set("if", "else", "for", "while", "do", "switch", "case", "default", "break", "continue", "return", "throw", "try", "catch", "finally",
	"function", "class", "interface", "enum", "type", "const", "let", "var", "new", "await", "yield", "async", "import", "export", "from",
	"extends", "implements", "typeof", "keyof", "in", "of", "instanceof", "as", "satisfies", "true", "false", "null", "undefined", "this")

const (
	nFunction = "function"
	nDeclared = "type"
	nHandler  = "handler"
	nTest     = "test"
	nModule   = "module"
)

func nodeKind(code []token, from, to int) string {
	h := head(code, from, to)
	if h == "function" {
		return nFunction
	}
	if declarers[h] {
		return nDeclared
	}
	if (h == "test" || h == "it" || h == "describe") && from+1 < to && code[from+1].text == "(" {
		return nTest
	}
	if h == "const" || h == "let" || h == "var" {
		for j := from; j < to; j++ {
			if code[j].text == "=>" || code[j].text == "function" && code[j].isIdent() {
				return nFunction
			}
		}
		return nDeclared
	}
	return nModule
}

func nameOf(code []token, from, to int) string {
	h := head(code, from, to)
	if h == "import" || h == "{" || h == "*" {
		return ""
	}
	for j := from; j < to-1; j++ {
		if code[j].text == h && code[j+1].isIdent() {
			return code[j+1].text
		}
	}
	return ""
}

func params(code []token, from, to int) int {
	for j := from; j < to; j++ {
		if code[j].text == "(" {
			end := closing(code, j, to)
			if end == j+1 {
				return 0
			}
			n, nest := 1, 0
			for k := j + 1; k < end; k++ {
				switch code[k].text {
				case "(", "[", "{", "<":
					nest++
				case ")", "]", "}", ">":
					nest--
				case ",":
					if nest == 0 && k+1 < end {
						n++
					}
				}
			}
			return n
		}
	}
	return 0
}

// shapeHash is each token's word hashed into the FNV-1a state: the
// trigram hash is three steps of it, so the word is looked up once.
func shapeWord(t *token) string {
	switch t.kind {
	case ident:
		if keywords[t.text] {
			return t.text
		}
		return "n"
	case literal:
		return "l"
	case punct:
		return t.text
	}
	return ""
}

func trigrams(words []string) []uint64 {
	if len(words) < 3 {
		return nil
	}
	out := make([]uint64, len(words)-2)
	for i := range out {
		h := uint64(14695981039346656037)
		for k := i; k < i+3; k++ {
			w := words[k]
			for c := 0; c < len(w); c++ {
				h = (h ^ uint64(w[c])) * 1099511628211
			}
			h = (h ^ '/') * 1099511628211
		}
		out[i] = h
	}
	return out
}

// votes counts, for each of the 64 bits, the runs that have it set, as
// bit-sliced counters: planes[j] holds bit j of all 64 counts, so adding
// a run is a ripple of carries over the planes (a few word operations)
// and not 64 adds. n is the runs counted.
type votes struct {
	planes []uint64
	n      int
}

func (v *votes) reset() {
	clear(v.planes)
	v.n = 0
}

// add counts one run.
func (v *votes) add(h uint64) {
	carry := h
	for j := 0; carry != 0; j++ {
		if j == len(v.planes) {
			v.planes = append(v.planes, 0)
		}
		t := v.planes[j] & carry
		v.planes[j] ^= carry
		carry = t
	}
	v.n++
}

// addVotes adds o's counts to v's, plane by plane with a carry.
func (v *votes) addVotes(o *votes) {
	var carry uint64
	for j := 0; j < len(o.planes) || carry != 0; j++ {
		if j == len(v.planes) {
			v.planes = append(v.planes, 0)
		}
		var y uint64
		if j < len(o.planes) {
			y = o.planes[j]
		}
		x := v.planes[j]
		v.planes[j] = x ^ y ^ carry
		carry = x&y | carry&(x^y)
	}
	v.n += o.n
}

// shape is the bits set in more of the counted runs than not: each
// count compared with n/2+1 at once, from the top plane down.
func (v *votes) shape() uint64 {
	m := v.n/2 + 1
	top := len(v.planes)
	if l := bits.Len(uint(m)); l > top {
		top = l
	}
	var gt uint64
	eq := ^uint64(0)
	for j := top - 1; j >= 0; j-- {
		var a, mb uint64
		if j < len(v.planes) {
			a = v.planes[j]
		}
		if (m>>j)&1 == 1 {
			mb = ^uint64(0)
		}
		gt |= eq & a &^ mb
		eq &^= a ^ mb
	}
	return gt | eq
}

// shape is the SimHash of the runs of code[from:to].
func shape(runs []uint64, from, to int) uint64 {
	var v votes
	for i := from; i < to-2; i++ {
		v.add(runs[i])
	}
	return v.shape()
}

func handlerShape(code []token, bound string, open, close int) string {
	if close == open+1 {
		return "empty"
	}
	for j := open + 1; j < close-1; j++ {
		if code[j].text == "throw" && bound != "" && code[j+1].text == bound {
			return "rethrows"
		}
	}
	last := open + 1
	for j := open + 1; j < close; j++ {
		if code[j].text == "return" {
			last = j
		}
	}
	if code[last].text == "return" && last+1 < close {
		if code[last+1].kind == literal {
			return "literal"
		}
		x := code[last+1].text
		if x == "[" && code[last+2].text == "]" || x == "{" && code[last+2].text == "}" || x == "null" || x == "undefined" || x == "true" || x == "false" {
			return "literal"
		}
	}
	for j := open + 1; j < close; j++ {
		if code[j].text == "(" && j > open+1 && code[j-1].isIdent() && !controls[code[j-1].text] {
			return "records"
		}
	}
	return "translates"
}

type catch struct {
	at, open, close int
	bound           string
}

func catches(code []token, from, to int) []catch {
	var out []catch
	for j := from; j < to; j++ {
		if code[j].text == "catch" && code[j].isIdent() && (j == 0 || code[j-1].text != ".") {
			k := j + 1
			bound := ""
			if k < to && code[k].text == "(" {
				if k+1 < to && code[k+1].isIdent() {
					bound = code[k+1].text
				}
				k = closing(code, k, to) + 1
			}
			if k < to && code[k].text == "{" {
				out = append(out, catch{j, k, closing(code, k, to), bound})
			}
		}
	}
	return out
}

func writeNode(w *bufio.Writer, starts []int, code []token, cs []catch, sh uint64, kind string, from, to int, name string, whole int) {
	line := lineOf(starts, code[from].from)
	lines, lastLine, deepest := 0, -1, 0
	l := line
	for j := from; j < to; j++ {
		l = lineFrom(starts, l, code[j].from)
		if l != lastLine {
			lines++
			lastLine = l
		}
		if d := code[j].depth - code[from].depth; d > deepest {
			deepest = d
		}
	}
	shapes := ""
	for _, c := range cs {
		if shapes != "" {
			shapes += ","
		}
		shapes += handlerShape(code, c.bound, c.open, c.close)
	}
	if shapes == "" {
		shapes = "-"
	}
	var nameWords uint64
	if name != "" {
		nameWords = wordsSketch(name, 0)
	}
	ps := 0
	if kind == nFunction || kind == nTest {
		ps = params(code, from, to)
	}
	offset, length, ln, col := code[from].from, code[to-1].to-code[from].from, line+1, code[from].from-starts[line]+1
	if whole >= 0 {
		offset, length, ln, col = 0, whole, 1, 1
	}
	fmt.Fprintf(w, "N %s %d %d %d %d ", kind, offset, length, ln, col)
	hex16(w, nameWords)
	fmt.Fprintf(w, " %d %d %d %s ", ps, lines, deepest, shapes)
	hex16(w, sh)
	w.WriteByte('\n')
}

func writeNodes(w *bufio.Writer, src string, starts []int, code []token, names map[string]bool) {
	if len(code) == 0 {
		return
	}
	words := make([]string, len(code))
	for i := range code {
		words[i] = shapeWord(&code[i])
	}
	runs := trigrams(words)
	// The statements' runs are counted once each, and the module's votes
	// are their votes plus the runs between statements.
	var stmts []span
	var shapes []uint64
	var module, stmt votes
	r := 0
	cursor := 0
	for _, s := range statements(code) {
		from := cursor
		for from < len(code) && code[from].from < s.from {
			from++
		}
		to := from
		for to < len(code) && code[to].from < s.to {
			to++
		}
		cursor = to
		for ; r < from && r < len(runs); r++ {
			module.add(runs[r])
		}
		stmt.reset()
		for i := from; i < to-2; i++ {
			stmt.add(runs[i])
		}
		module.addVotes(&stmt)
		r = max(r, to-2)
		stmts = append(stmts, span{from, to})
		shapes = append(shapes, stmt.shape())
	}
	for ; r < len(runs); r++ {
		module.add(runs[r])
	}
	all := catches(code, 0, len(code))
	writeNode(w, starts, code, all, module.shape(), nModule, 0, len(code), "", len(src))
	for i, s := range stmts {
		kind := nodeKind(code, s.from, s.to)
		name := nameOf(code, s.from, s.to)
		if names[name] {
			name = ""
		}
		writeNode(w, starts, code, catches(code, s.from, s.to), shapes[i], kind, s.from, s.to, name, -1)
	}
	for _, c := range all {
		writeNode(w, starts, code, catches(code, c.at, c.close+1), shape(runs, c.at, c.close+1), nHandler, c.at, c.close+1, "", -1)
	}
}

func main() {
	if len(os.Args) != 3 || os.Args[1] != "record" {
		fmt.Fprintln(os.Stderr, "usage: control record FILE")
		os.Exit(2)
	}
	b, err := os.ReadFile(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	w := bufio.NewWriter(os.Stdout)
	record(w, string(b))
	w.Flush()
}
