package bindc

import (
	"regexp"
	"strings"
)

// The directive pass reads every preprocessing directive in order and
// leaves the C tokens for the declaration parser. It accepts the include
// guard, the three standard includes, the `#ifdef __cplusplus` blocks, and
// object-like macros with a constant body; every other directive is one
// diagnostic. A refused conditional is skipped up to its #endif, so its
// body gives no second diagnostic.

type macroDef struct {
	pos   Pos
	value cval
	// expansion is the replacement tokens of the body, with the names of
	// earlier constants already replaced.
	expansion []token
	index     int // in pre.constants; -1 for the include guard
}

type pre struct {
	d         *diags
	guard     string
	guardPos  Pos
	cplusplus bool
	includes  map[string]Pos
	order     []string
	constants []Constant
	macros    map[string]macroDef
	ctoks     []token
}

// directive returns the tokens of the directive that starts at toks[i]
// (its `#`), without the `#`, and the index after it.
func directive(toks []token, i int) ([]token, int) {
	j := i + 1
	for toks[j].kind != tEOF && !toks[j].bol {
		j++
	}
	return toks[i+1 : j], j
}

func isDirective(toks []token, i int) bool { return toks[i].bol && toks[i].is("#") }

func words(ts []token) string {
	s := make([]string, len(ts))
	for i, t := range ts {
		s[i] = t.text
	}
	return strings.Join(s, " ")
}

func runDirectives(toks []token, d *diags) *pre {
	p := &pre{d: d, includes: map[string]Pos{}, macros: map[string]macroDef{}}
	i := 0
	// The include guard: the file starts with `#ifndef G` and `#define G`.
	guardOpen := false
	if isDirective(toks, 0) {
		a, j := directive(toks, 0)
		if len(a) == 2 && a[0].is("ifndef") && a[1].kind == tIdent && isDirective(toks, j) {
			b, k := directive(toks, j)
			if len(b) == 2 && b[0].is("define") && b[1].is(a[1].text) {
				p.guard, p.guardPos, guardOpen = a[1].text, toks[0].pos, true
				p.macros[p.guard] = macroDef{pos: b[1].pos, index: -1}
				i = k
			}
		}
	}
	cppOpen, cppClosed := false, false
	var cppOpenPos Pos
	sawC := false
	closedAt := "" // what closed the declarations: "guard" or "cplusplus"
	for toks[i].kind != tEOF {
		if !isDirective(toks, i) {
			if closedAt != "" {
				d.add(toks[i].pos, CodeStructure, "declaration after the close of the %s", closedAt)
				for toks[i].kind != tEOF && !isDirective(toks, i) {
					i++
				}
				continue
			}
			p.ctoks = append(p.ctoks, toks[i])
			sawC = true
			i++
			continue
		}
		hash := toks[i]
		dir, j := directive(toks, i)
		name := ""
		if len(dir) > 0 {
			name = dir[0].text
		}
		switch {
		case name == "endif" && len(dir) == 1 && guardOpen && closedAt != "guard":
			if cppOpen && !cppClosed {
				d.add(hash.pos, CodeStructure, "the include guard closes before the extern \"C\" block")
			}
			closedAt = "guard"
			i = j
		case name == "ifdef" && len(dir) == 2 && dir[1].is("__cplusplus"):
			body := []token{}
			k := j
			for toks[k].kind != tEOF && !isDirective(toks, k) {
				body = append(body, toks[k])
				k++
			}
			end, after := []token(nil), k
			if isDirective(toks, k) {
				end, after = directive(toks, k)
			}
			ok := len(end) == 1 && end[0].is("endif")
			open := ok && len(body) == 3 && body[0].is("extern") && body[1].kind == tString && body[1].text == `"C"` && body[2].is("{")
			closer := ok && len(body) == 1 && body[0].is("}")
			switch {
			case open && !cppOpen && !sawC && closedAt == "":
				cppOpen, cppOpenPos = true, hash.pos
			case closer && cppOpen && !cppClosed && closedAt == "":
				cppClosed = true
				closedAt = "extern \"C\" block"
			case open || closer:
				d.add(hash.pos, CodeStructure, "the extern \"C\" block opens once before every declaration and closes once after them")
			default:
				d.add(hash.pos, CodeConditional, "`#ifdef __cplusplus` holds only `extern \"C\" {` or `}`")
				after = skipConditional(toks, i)
			}
			i = after
		case name == "if" || name == "ifdef" || name == "ifndef":
			d.add(hash.pos, CodeConditional, "`#%s` is outside the subset", name)
			i = skipConditional(toks, i)
		case name == "elif" || name == "else" || name == "endif" || name == "elifdef" || name == "elifndef":
			d.add(hash.pos, CodeConditional, "`#%s` without an accepted `#if`", name)
			i = j
		case name == "include":
			p.include(hash, dir)
			i = j
		case name == "define":
			if closedAt == "guard" {
				d.add(hash.pos, CodeStructure, "macro after the close of the include guard")
			} else {
				last := hash
				if len(dir) > 0 {
					last = dir[len(dir)-1]
				}
				// A missing operand is reported just after the body.
				p.define(hash, dir, Pos{last.pos.File, last.pos.Line, last.pos.Col + len(last.text)})
			}
			i = j
		default:
			d.add(hash.pos, CodeDirective, "`#%s` is outside the subset", words(dir))
			i = j
		}
	}
	if guardOpen && closedAt != "guard" {
		d.add(p.guardPos, CodeStructure, "the include guard has no #endif")
	}
	if cppOpen && !cppClosed {
		d.add(cppOpenPos, CodeStructure, "the extern \"C\" block is not closed")
	}
	p.cplusplus = cppOpen
	p.ctoks = append(p.ctoks, toks[len(toks)-1])
	return p
}

// skipConditional returns the index after the #endif that closes the
// conditional directive at toks[i], or the end of the file.
func skipConditional(toks []token, i int) int {
	depth := 0
	for toks[i].kind != tEOF {
		if !isDirective(toks, i) {
			i++
			continue
		}
		dir, j := directive(toks, i)
		if len(dir) > 0 {
			switch dir[0].text {
			case "if", "ifdef", "ifndef":
				depth++
			case "endif":
				depth--
				if depth == 0 {
					return j
				}
			}
		}
		i = j
	}
	return i
}

var standardHeaders = map[string]bool{"stdint.h": true, "stdbool.h": true, "stddef.h": true}

func (p *pre) include(hash token, dir []token) {
	name := ""
	ok := len(dir) >= 3 && dir[1].is("<") && dir[len(dir)-1].is(">")
	if ok {
		for k := 2; k < len(dir)-1; k++ {
			prev := dir[k-1]
			if dir[k].pos.Line != prev.pos.Line || dir[k].pos.Col != prev.pos.Col+len(prev.text) {
				ok = false
			}
			name += dir[k].text
		}
		last := dir[len(dir)-2]
		if dir[len(dir)-1].pos.Col != last.pos.Col+len(last.text) {
			ok = false
		}
	}
	if !ok || !standardHeaders[name] {
		p.d.add(hash.pos, CodeDirective, "`#%s` is outside the subset (only <stdint.h>, <stdbool.h> and <stddef.h>)", words(dir))
		return
	}
	if _, dup := p.includes[name]; dup {
		p.d.add(hash.pos, CodeDuplicate, "<%s> is included twice", name)
		return
	}
	p.includes[name] = hash.pos
	p.order = append(p.order, name)
}

// included reports whether header is included before pos.
func (p *pre) included(header string, pos Pos) bool {
	at, ok := p.includes[header]
	return ok && at.before(pos)
}

// reservedMacro matches the names a header of the subset may not define or
// declare: reserved identifiers (C11 7.1.3: `__x`, `_X`), which include
// the compilers' predefined macros, the GNU predefined `linux`, `unix` and
// `i386`, and the macros of <stdint.h>, <stdbool.h> and <stddef.h>. A C
// compiler would expand such a name where the parser sees an identifier.
var reservedMacro = regexp.MustCompile(`^(_[A-Z_].*|linux|unix|i386|U?INT[0-9]+_(C|MAX|MIN)|U?INT(_LEAST|_FAST)[0-9]+_(MAX|MIN)|U?INT(PTR|MAX)_(C|MAX|MIN)|(SIZE|PTRDIFF|SIG_ATOMIC|WCHAR|WINT)_(MAX|MIN)|NULL|offsetof|bool|true|false)$`)

func (p *pre) define(hash token, dir []token, end Pos) {
	if len(dir) < 2 || dir[1].kind != tIdent {
		p.d.add(hash.pos, CodeDirective, "`#define` needs a name")
		return
	}
	nameTok := dir[1]
	name := nameTok.text
	body := dir[2:]
	if len(body) > 0 && body[0].is("(") && body[0].pos.Line == nameTok.pos.Line && body[0].pos.Col == nameTok.pos.Col+len(name) {
		p.d.add(nameTok.pos, CodeFunctionMacro, "function-like macro %s", name)
		return
	}
	if keywords[name] || scalarNames[name] || reservedMacro.MatchString(name) {
		p.d.add(nameTok.pos, CodeMacroIdentifier, "macro %s redefines a keyword, a standard type or a reserved name", name)
		return
	}
	if prev, dup := p.macros[name]; dup {
		p.d.add(nameTok.pos, CodeDuplicate, "macro %s is already defined at %s", name, prev.pos)
		return
	}
	if len(body) == 0 {
		p.d.add(nameTok.pos, CodeMacroBody, "macro %s has no body", name)
		return
	}
	macro := func(n string) ([]token, bool) {
		m, ok := p.macros[n]
		if !ok || m.index < 0 {
			return nil, false
		}
		return m.expansion, true
	}
	stdint := func(pos Pos) bool { return p.included("stdint.h", pos) }
	e, expansion, v, err := parseConstExpr(body, end, macro, stdint)
	if err != nil {
		p.d.add(err.pos, err.code, "%s", err.msg)
		return
	}
	p.macros[name] = macroDef{pos: nameTok.pos, value: v, expansion: expansion, index: len(p.constants)}
	p.constants = append(p.constants, Constant{Name: name, Type: v.typ, Bits: bitsOf(v), Expr: e.render()})
}
