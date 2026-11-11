package regexpplan

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"strings"
)

// EmitGo returns a self-contained Go file with Match. The result is start/end,
// ordered (signed capture tag, byte position) events, and success. The caller
// owns nominal adapters. There are no imports, globals or runtime pattern parses.
func (p *Plan) EmitGo() (string, error) {
	src, err := p.emit(p.info.Engine)
	if err == nil {
		err = p.ValidateGo(src)
	}
	return src, err
}

func actionLiteral(tags []int) string {
	var b []byte
	for _, tag := range tags {
		b = binary.LittleEndian.AppendUint16(b, uint16(int16(tag)))
	}
	return strconv.Quote(string(b))
}

func directStep(c *candidate) string {
	var b strings.Builder
	b.WriteString("func step(q,b int)(int,string,bool){switch q {")
	for q, s := range c.states {
		fmt.Fprintf(&b, "case %d: switch b {", q)
		// Group equal transitions without changing their action program.
		done := [257]bool{}
		for v, e := range s.edges {
			if done[v] || (e.to < 0 && !e.accept) {
				continue
			}
			b.WriteString("case ")
			first := true
			for w := v; w <= 256; w++ {
				x := s.edges[w]
				if x.to == e.to && x.accept == e.accept && slices.Equal(x.tags, e.tags) {
					if !first {
						b.WriteByte(',')
					}
					fmt.Fprintf(&b, "%d", w)
					first, done[w] = false, true
				}
			}
			fmt.Fprintf(&b, ": return %d,%s,%t;", e.to, actionLiteral(e.tags), e.accept)
		}
		b.WriteString("};")
	}
	b.WriteString("};return -1,\"\",false}")
	return b.String()
}

func tableSize(c *candidate) int {
	offsets, data := compactTable(c)
	return len(offsets) + len(data)
}

// The table form uses an offset table plus a deduplicated action/edge string.
// Only literals are materialized; there is no mutable matcher initialization.
func compactTable(c *candidate) (string, string) {
	var offsets, data []byte
	known := map[string]int{}
	for _, s := range c.states {
		for _, e := range s.edges {
			entry := binary.LittleEndian.AppendUint16(nil, uint16(e.to+1))
			a := byte(0)
			if e.accept {
				a = 1
			}
			entry = append(entry, a)
			entry = binary.LittleEndian.AppendUint16(entry, uint16(len(e.tags)))
			for _, tag := range e.tags {
				entry = binary.LittleEndian.AppendUint16(entry, uint16(int16(tag)))
			}
			key := string(entry)
			off, ok := known[key]
			if !ok {
				off = len(data)
				known[key] = off
				data = append(data, entry...)
			}
			offsets = binary.LittleEndian.AppendUint32(offsets, uint32(off))
		}
	}
	return string(offsets), string(data)
}

const tableStep = `
func step(q,b int)(int,string,bool) {
 const offsets = OFFSETS
 const data = DATA
 k := (q*257+b)*4
 o := int(uint32(offsets[k])|uint32(offsets[k+1])<<8|uint32(offsets[k+2])<<16|uint32(offsets[k+3])<<24)
 to := int(data[o])|int(data[o+1])<<8
 accept := data[o+2] != 0
 n := int(data[o+3])|int(data[o+4])<<8
 return to-1,data[o+5:o+5+n*2],accept
}`

const deterministicMatch = `
func Match(s string)(int,int,[][2]int,bool) {
 const find = FIND
 last := 0
 if find {last=len(s)}
 for start:=0;start<=last;start++ {
  q:=0
  var events [][2]int
  for pos:=start;;pos++ {
   b:=256
   if pos<len(s) {b=int(s[pos])}
   to,tags,accept:=step(q,b)
   if accept && (find || pos==len(s)) {
    for j:=0;j<len(tags);j+=2 {
     tag:=int(int16(uint16(tags[j])|uint16(tags[j+1])<<8))
     events=append(events,[2]int{tag,pos})
    }
    return start,pos,events,true
   }
   if to<0 || pos==len(s) {break}
   for j:=0;j<len(tags);j+=2 {
    tag:=int(int16(uint16(tags[j])|uint16(tags[j+1])<<8))
    events=append(events,[2]int{tag,pos})
   }
   q=to
  }
 }
 return 0,0,nil,false
}`

// NFA uses the very same instruction graph as Reference, not a second regex
// compiler. Immutable history links retain every event when threads fork.
const nfaMatch = `
func Match(s string)(int,int,[][2]int,bool) {
 const find = FIND
 const initial = INITIAL
 type instruction struct {op int;x int;y int;tag int;set [4]uint64}
 code := [...]instruction{PROGRAM}
 type history struct {event [2]int;prev *history}
 type thread struct {pc int;h *history}
 last:=0
 if find {last=len(s)}
 for start:=0;start<=last;start++ {
  current:=[]thread{{initial,nil}}
  bestStart,bestEnd:=0,0
  var best [][2]int
  ok:=false
  for pos:=start;;pos++ {
   seen:=make([]bool,len(code))
   var ready []thread
   var visit func(thread)
   visit=func(t thread) {
    if seen[t.pc] {return}
    seen[t.pc]=true
    i:=code[t.pc]
    switch i.op {
    case 2: visit(thread{i.x,t.h});visit(thread{i.y,t.h})
    case 4: visit(thread{i.x,t.h})
    case 3: visit(thread{i.x,&history{[2]int{i.tag,pos},t.h}})
    default: ready=append(ready,t)
    }
   }
   for _,t:=range current {visit(t)}
   var next []thread
   for _,t:=range ready {
    i:=code[t.pc]
    if i.op==0 && (find || pos==len(s)) {
     bestStart,bestEnd,ok=start,pos,true
     n:=0
     for h:=t.h;h!=nil;h=h.prev {n++}
     best=make([][2]int,n)
     h:=t.h
     for j:=n-1;j>=0;j-- {best[j]=h.event;h=h.prev}
     break
    }
    if i.op==1 && pos<len(s) && i.set[s[pos]/64]&(1<<(s[pos]%64))!=0 {
     next=append(next,thread{i.x,t.h})
    }
   }
   if pos==len(s) || len(next)==0 {break}
   current=next
  }
  if ok {return bestStart,bestEnd,best,true}
 }
 return 0,0,nil,false
}`

func (p *Plan) emit(engine Engine) (string, error) {
	find := strconv.FormatBool(p.mode == Find)
	src := "package pattern\n"
	switch engine {
	case Direct, Table:
		if err := validate(p, p.ir); err != nil {
			return "", err
		}
		if engine == Direct {
			src += directStep(p.ir)
		} else {
			offsets, data := compactTable(p.ir)
			src += strings.NewReplacer("OFFSETS", strconv.Quote(offsets), "DATA", strconv.Quote(data)).Replace(tableStep)
		}
		src += strings.ReplaceAll(deterministicMatch, "FIND", find)
	case NFA:
		var program strings.Builder
		for _, i := range p.code {
			fmt.Fprintf(&program, "{%d,%d,%d,%d,[4]uint64{%d,%d,%d,%d}},", i.op, i.x, i.y, i.tag, i.set[0], i.set[1], i.set[2], i.set[3])
		}
		src += strings.NewReplacer("FIND", find, "INITIAL", strconv.Itoa(p.start), "PROGRAM", program.String()).Replace(nfaMatch)
	default:
		return "", fmt.Errorf("unknown engine")
	}
	formatted, err := format.Source([]byte(src))
	return string(formatted), err
}

func astText(n ast.Node) string {
	var b bytes.Buffer
	_ = format.Node(&b, token.NewFileSet(), n)
	return b.String()
}

func intExpr(e ast.Expr) (int, error) {
	text := astText(e)
	n, err := strconv.ParseInt(text, 10, 64)
	return int(n), err
}

func stringConstant(fn *ast.FuncDecl, name string) (string, error) {
	for _, stmt := range fn.Body.List {
		if d, ok := stmt.(*ast.DeclStmt); ok {
			for _, spec := range d.Decl.(*ast.GenDecl).Specs {
				v := spec.(*ast.ValueSpec)
				if len(v.Names) == 1 && v.Names[0].Name == name && len(v.Values) == 1 {
					return strconv.Unquote(astText(v.Values[0]))
				}
			}
		}
	}
	return "", fmt.Errorf("missing %s constant", name)
}

// readDirect is a small evaluator for the restricted emitted step language:
// integer switch cases and literal returns only. It reads the actual Go AST,
// rejecting arbitrary calls, mutation, duplicate cases and hidden defaults.
func readDirect(fn *ast.FuncDecl) (map[int][257]edge, error) {
	var reject map[int][257]edge
	if len(fn.Body.List) != 2 {
		return reject, fmt.Errorf("invalid direct step body")
	}
	outer, ok := fn.Body.List[0].(*ast.SwitchStmt)
	if !ok || astText(outer.Tag) != "q" || outer.Init != nil || astText(fn.Body.List[1]) != `return -1, "", false` {
		return reject, fmt.Errorf("invalid direct dispatcher")
	}
	result := map[int][257]edge{}
	states := map[int]bool{}
	for _, clause := range outer.Body.List {
		c, ok := clause.(*ast.CaseClause)
		if !ok || len(c.List) != 1 || len(c.Body) != 1 {
			return reject, fmt.Errorf("invalid state clause")
		}
		id, err := intExpr(c.List[0])
		if err != nil || id < 0 || states[id] {
			return reject, fmt.Errorf("invalid/duplicate state")
		}
		states[id] = true
		row := [257]edge{}
		for b := range row {
			row[b].to = -1
		}
		inner, ok := c.Body[0].(*ast.SwitchStmt)
		if !ok || astText(inner.Tag) != "b" || inner.Init != nil {
			return reject, fmt.Errorf("invalid byte dispatcher")
		}
		seen := map[int]bool{}
		for _, clause := range inner.Body.List {
			c, ok := clause.(*ast.CaseClause)
			if !ok || len(c.List) == 0 || len(c.Body) != 1 {
				return reject, fmt.Errorf("invalid byte clause")
			}
			ret, ok := c.Body[0].(*ast.ReturnStmt)
			if !ok || len(ret.Results) != 3 {
				return reject, fmt.Errorf("invalid edge return")
			}
			to, err := intExpr(ret.Results[0])
			if err != nil {
				return reject, err
			}
			tags, err := strconv.Unquote(astText(ret.Results[1]))
			if err != nil || len(tags)%2 != 0 {
				return reject, fmt.Errorf("invalid tag program")
			}
			e := edge{to: to}
			for j := 0; j < len(tags); j += 2 {
				e.tags = append(e.tags, int(int16(binary.LittleEndian.Uint16([]byte(tags[j:j+2])))))
			}
			a := astText(ret.Results[2])
			if a != "true" && a != "false" {
				return reject, fmt.Errorf("nonliteral acceptance")
			}
			e.accept = a == "true"
			for _, expr := range c.List {
				v, err := intExpr(expr)
				if err != nil || v < 0 || v > 256 || seen[v] {
					return reject, fmt.Errorf("invalid/duplicate byte case")
				}
				seen[v] = true
				row[v] = e
			}
		}
		result[id] = row
	}
	return result, nil
}

func readTable(offsets, data string, q, b int) (edge, error) {
	bad := edge{to: -1}
	k := (q*257 + b) * 4
	if k+4 > len(offsets) {
		return bad, fmt.Errorf("truncated offset table")
	}
	o := int(binary.LittleEndian.Uint32([]byte(offsets[k : k+4])))
	if o+5 > len(data) {
		return bad, fmt.Errorf("truncated edge table")
	}
	to := int(binary.LittleEndian.Uint16([]byte(data[o:o+2]))) - 1
	if data[o+2] > 1 {
		return bad, fmt.Errorf("invalid acceptance flag")
	}
	e := edge{to: to, accept: data[o+2] == 1}
	n := int(binary.LittleEndian.Uint16([]byte(data[o+3 : o+5])))
	if o+5+2*n > len(data) {
		return bad, fmt.Errorf("truncated tag program")
	}
	for j := 0; j < n; j++ {
		k := o + 5 + 2*j
		e.tags = append(e.tags, int(int16(binary.LittleEndian.Uint16([]byte(data[k:k+2])))))
	}
	return e, nil
}

// ValidateGo checks the actual serialization before it can leave this package.
// Direct switches are interpreted for all bytes+EOF and compared to canonical
// NFA edges. Tables are decoded from their actual literals. Fixed driver/decoder
// control flow must match the reviewed restricted templates; arbitrary Go is not
// admitted. NFA data and start are checked against the canonical graph.
func (p *Plan) ValidateGo(src string) error {
	file, err := parser.ParseFile(token.NewFileSet(), "pattern.go", src, 0)
	if err != nil {
		return err
	}
	if file.Name.Name != "pattern" || len(file.Imports) != 0 {
		return fmt.Errorf("invalid emitted file")
	}
	// NFA has no optimization correspondence. Exact canonical instruction
	// literals and the reviewed driver are its restricted representation.
	if len(file.Decls) == 1 {
		fn, ok := file.Decls[0].(*ast.FuncDecl)
		if !ok {
			return fmt.Errorf("invalid NFA declaration")
		}
		var program *ast.CompositeLit
		ast.Inspect(fn, func(n ast.Node) bool {
			a, ok := n.(*ast.AssignStmt)
			if ok && len(a.Lhs) == 1 && astText(a.Lhs[0]) == "code" && len(a.Rhs) == 1 {
				program, _ = a.Rhs[0].(*ast.CompositeLit)
			}
			return true
		})
		if program == nil || len(program.Elts) != len(p.code) {
			return fmt.Errorf("invalid NFA program")
		}
		for pc, expr := range program.Elts {
			lit, ok := expr.(*ast.CompositeLit)
			if !ok || len(lit.Elts) != 5 {
				return fmt.Errorf("invalid NFA instruction")
			}
			i := p.code[pc]
			want := []int{int(i.op), i.x, i.y, i.tag}
			for j, value := range want {
				got, err := intExpr(lit.Elts[j])
				if err != nil || got != value {
					return fmt.Errorf("NFA instruction mismatch at %d/%d", pc, j)
				}
			}
			set, ok := lit.Elts[4].(*ast.CompositeLit)
			if !ok || len(set.Elts) != 4 {
				return fmt.Errorf("invalid byte set")
			}
			for j, value := range i.set {
				got, err := strconv.ParseUint(astText(set.Elts[j]), 10, 64)
				if err != nil || got != value {
					return fmt.Errorf("NFA byte-set mismatch")
				}
			}
		}
		// Erase only the checked data. All remaining control flow, start,
		// profile and bounds tests must be the reviewed NFA driver.
		program.Elts = nil
		want := "package pattern\n" + strings.NewReplacer("FIND", strconv.FormatBool(p.mode == Find), "INITIAL", strconv.Itoa(p.start), "PROGRAM", "").Replace(nfaMatch)
		if astText(file) != canonicalText(want) {
			return fmt.Errorf("NFA serialization mismatch")
		}
		return nil
	}
	if len(file.Decls) != 2 || p.ir == nil {
		return fmt.Errorf("invalid emission declarations")
	}
	step, ok := file.Decls[0].(*ast.FuncDecl)
	if !ok || step.Recv != nil || astText(step.Type) != "func(q, b int) (int, string, bool)" || step.Name.Name != "step" {
		return fmt.Errorf("invalid step signature")
	}
	driver, ok := file.Decls[1].(*ast.FuncDecl)
	if !ok || astText(driver) != canonicalDecl(strings.ReplaceAll(deterministicMatch, "FIND", strconv.FormatBool(p.mode == Find))) {
		return fmt.Errorf("driver control-flow mismatch")
	}
	offsets, tableErr := stringConstant(step, "offsets")
	var data string
	var direct map[int][257]edge
	if tableErr == nil {
		data, err = stringConstant(step, "data")
		if err != nil {
			return err
		}
		want := strings.NewReplacer("OFFSETS", strconv.Quote(offsets), "DATA", strconv.Quote(data)).Replace(tableStep)
		if astText(step) != canonicalDecl(want) || len(offsets) != len(p.ir.states)*257*4 {
			return fmt.Errorf("table decoder mismatch")
		}
	} else {
		direct, err = readDirect(step)
		if err != nil {
			return err
		}
		if len(direct) != len(p.ir.states) {
			return fmt.Errorf("direct state count mismatch")
		}
	}
	actual := &candidate{states: make([]state, len(p.ir.states))}
	for q, s := range p.ir.states {
		actual.states[q].pc = s.pc
		for b := 0; b <= 256; b++ {
			var e edge
			if tableErr == nil {
				e, err = readTable(offsets, data, q, b)
			} else {
				row, ok := direct[q]
				if !ok {
					return fmt.Errorf("missing direct state")
				}
				e = row[b]
			}
			if err != nil {
				return err
			}
			actual.states[q].edges[b] = e
		}
	}
	return validate(p, actual)
}

func canonicalText(src string) string {
	f, err := parser.ParseFile(token.NewFileSet(), "pattern.go", src, 0)
	if err != nil {
		return ""
	}
	return astText(f)
}

func canonicalDecl(src string) string {
	f, err := parser.ParseFile(token.NewFileSet(), "pattern.go", "package pattern\n"+src, 0)
	if err != nil || len(f.Decls) != 1 {
		return ""
	}
	return astText(f.Decls[0])
}
