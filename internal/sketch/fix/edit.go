package fix

import (
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/format"
	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
)

// edit replaces the bytes [from, to) of a file's text with text.
type edit struct {
	from, to int
	text     string
	seq      int
}

// editor holds each read file's text, its parsed tree and its edits.
type editor struct {
	texts   map[string]string
	parsed  map[string]*syntax.File
	edits   map[string][]edit
	clauses map[*syntax.TypeDecl]int
	// included and claused are the texts already written per
	// declaration: two pairs that spell alike write once.
	included map[*syntax.TypeDecl]map[string]bool
	claused  map[*syntax.TypeDecl]map[string]bool
	imports  map[string][]string
	seq      int
}

func (e *editor) read(file string) (string, *syntax.File) {
	if f, ok := e.parsed[file]; ok {
		return e.texts[file], f
	}
	src, err := os.ReadFile(file)
	if err != nil {
		e.parsed[file] = nil
		return "", nil
	}
	f, err := syntax.Parse(string(src))
	if err != nil {
		f = nil
	}
	e.texts[file], e.parsed[file] = string(src), f
	return string(src), f
}

// find is the file among files that declares the type name, and the
// declaration.
func (e *editor) find(files []string, name string) (string, *syntax.TypeDecl) {
	for _, file := range files {
		_, f := e.read(file)
		if f == nil {
			continue
		}
		for _, d := range f.Decls {
			if td, ok := d.(*syntax.TypeDecl); ok && td.Name.Name == name {
				return file, td
			}
		}
	}
	return "", nil
}

func (e *editor) add(file string, ed edit) {
	if e.edits == nil {
		e.edits = map[string][]edit{}
	}
	e.seq++
	ed.seq = e.seq
	e.edits[file] = append(e.edits[file], ed)
}

// clause appends text to the declaration's implements clause, or writes
// the clause after its body.
func (e *editor) clause(file string, d *syntax.TypeDecl, text string) {
	if e.clauses == nil {
		e.clauses = map[*syntax.TypeDecl]int{}
		e.claused = map[*syntax.TypeDecl]map[string]bool{}
	}
	if e.claused[d] == nil {
		e.claused[d] = map[string]bool{}
	}
	if e.claused[d][text] {
		return
	}
	src := e.texts[file]
	for _, x := range d.Implements {
		if src[int(x.Pos()):int(x.End())] == text {
			return
		}
	}
	e.claused[d][text] = true
	at := int(d.End())
	sep := " implements "
	if len(d.Implements) > 0 || e.clauses[d] > 0 {
		sep = ", "
	}
	e.clauses[d]++
	e.add(file, edit{from: at, to: at, text: sep + text})
}

// include writes the member `..text` first in the operation type d, and
// removes d's own operations that op declares (sameOps checked that they
// are the same operations).
func (e *editor) include(file string, d *syntax.TypeDecl, text string, op check.Type) error {
	spec, ok := d.Spec.(*syntax.InterfaceType)
	if !ok || !spec.Lbrace.IsValid() {
		return fmt.Errorf("%s is not an operation type with braces", d.Name.Name)
	}
	n, ok := op.(*check.Named)
	if !ok {
		return fmt.Errorf("the included type is not a named operation type")
	}
	if e.included == nil {
		e.included = map[*syntax.TypeDecl]map[string]bool{}
	}
	if e.included[d] == nil {
		e.included[d] = map[string]bool{}
	}
	if e.included[d][text] {
		return nil
	}
	e.included[d][text] = true
	it, ok := n.Underlying().(*check.Interface)
	if !ok {
		return fmt.Errorf("%s is not an operation type", op)
	}
	names := map[string]bool{}
	for _, m := range it.Methods {
		names[m.Name] = true
	}
	src, _ := e.read(file)
	if !strings.Contains(src[spec.Lbrace:spec.Rbrace], "\n") {
		// A body on one line stays on one line: `{ ..I, Other() }`.
		parts := []string{".." + text}
		for _, inc := range spec.Includes {
			parts = append(parts, src[int(inc.Pos()):int(inc.End())])
		}
		for _, m := range spec.Methods {
			if len(m.Names) > 0 && names[m.Names[0].Name] {
				continue
			}
			parts = append(parts, src[int(m.Pos()):int(m.End())])
		}
		for _, l := range spec.Laws {
			parts = append(parts, src[int(l.Pos()):int(l.End())])
		}
		if len(e.included[d]) > 1 {
			return fmt.Errorf("%s is on one line and includes two operation types: write it by hand", d.Name.Name)
		}
		e.add(file, edit{from: int(spec.Lbrace) + 1, to: int(spec.Rbrace), text: " " + strings.Join(parts, ", ") + " "})
		return nil
	}
	for _, m := range spec.Methods {
		if len(m.Names) == 0 || !names[m.Names[0].Name] {
			continue
		}
		from := int(m.Names[0].Pos())
		if m.Doc != nil {
			from = int(m.Doc.Pos())
		}
		to := int(m.Type.End())
		i := to
		for i < len(src) && (src[i] == ' ' || src[i] == '\t') {
			i++
		}
		if i < len(src) && src[i] == ',' {
			to = i + 1
		}
		// The whole line when the member is alone on its lines.
		ls := strings.LastIndexByte(src[:from], '\n') + 1
		le := strings.IndexByte(src[to:], '\n')
		if strings.TrimSpace(src[ls:from]) == "" && le >= 0 && strings.TrimSpace(src[to:to+le]) == "" {
			from, to = ls, to+le+1
		}
		e.add(file, edit{from: from, to: to})
	}
	at := int(spec.Lbrace) + 1
	e.add(file, edit{from: at, to: at, text: "\n\t.." + text + ","})
	return nil
}

// spell is the operation type op as the file declaring a type of package
// pkg names it, the import the file needs for it ("" for none), or why it
// cannot be named there. pkgFiles are the package's files: an import that
// another file of the package has is added to this one.
func (e *editor) spell(file string, pkgFiles []string, pkg, typeName string, op check.Type) (text, imp, why string) {
	_, f := e.read(file)
	if f == nil {
		return "", "", "its file does not parse"
	}
	var need []string
	var spellType func(t check.Type) (string, string)
	spellType = func(t check.Type) (string, string) {
		switch t := t.(type) {
		case *check.Named:
			o := t
			if t.Orig != nil {
				o = t.Orig
			}
			if o.Obj == nil || o.Obj.Pkg == nil {
				return "", fmt.Sprintf("%s has no package", t)
			}
			name := o.Obj.Name
			switch p := o.Obj.Pkg; {
			case p.Path == pkg && !p.Host():
			case p.Path == std.PreludePath:
				if e.declares(pkgFiles, name) && name != typeName {
					return "", fmt.Sprintf("the package declares its own %s, which hides the prelude's", name)
				}
			default:
				local, ok := importName(f, p.Path, p.Host())
				if !ok {
					spec, found := e.otherImport(pkgFiles, p.Path, p.Host())
					if !found {
						return "", fmt.Sprintf("package %s does not import %s: import it and write the clause, or wrap the value in a record that implements %s", pkg, p.Path, t)
					}
					need = append(need, spec)
					local = specLocal(spec, p.Path, p.Host())
				}
				name = local + "." + name
			}
			if len(t.TArgs) > 0 {
				var args []string
				for _, a := range t.TArgs {
					s, why := spellType(a)
					if why != "" {
						return "", why
					}
					args = append(args, s)
				}
				name += "[" + strings.Join(args, ", ") + "]"
			}
			return name, ""
		case *check.TypeParam:
			return t.Name, ""
		case *check.Basic:
			return t.String(), ""
		case *check.Slice:
			s, why := spellType(t.Elem)
			return "[]" + s, why
		case *check.Map:
			k, why := spellType(t.Key)
			if why != "" {
				return "", why
			}
			v, why := spellType(t.Elem)
			return "Map[" + k + ", " + v + "]", why
		}
		return "", fmt.Sprintf("the type argument %s has no spelling here", t)
	}
	text, why = spellType(op)
	if why != "" {
		return "", "", why
	}
	if len(need) > 0 {
		imp = need[0]
	}
	return text, imp, ""
}

// declares reports whether a file of the package declares a type name.
func (e *editor) declares(files []string, name string) bool {
	_, d := e.find(files, name)
	return d != nil
}

// importName is the name the file binds for the import path, if it imports
// it.
func importName(f *syntax.File, p string, host bool) (string, bool) {
	for _, d := range f.Decls {
		id, ok := d.(*syntax.ImportDecl)
		if !ok {
			continue
		}
		for _, s := range id.Specs {
			if s.Path == nil || s.Path.Value != p || (s.Kind == syntax.ImportGo) != host {
				continue
			}
			if s.Name != nil {
				return s.Name.Name, true
			}
			return defaultName(p, host), true
		}
	}
	return "", false
}

func defaultName(p string, host bool) string {
	if host {
		return check.GoPackageName(p)
	}
	return path.Base(p)
}

// otherImport is the import line another file of the package writes for
// the path.
func (e *editor) otherImport(files []string, p string, host bool) (string, bool) {
	for _, file := range files {
		_, f := e.read(file)
		if f == nil {
			continue
		}
		if local, ok := importName(f, p, host); ok {
			line := "import "
			if host {
				line += "go "
			}
			if local != defaultName(p, host) {
				line += local + " "
			}
			return line + p, true
		}
	}
	return "", false
}

// specLocal is the name an import line binds.
func specLocal(line, p string, host bool) string {
	fields := strings.Fields(line)
	if host {
		fields = fields[1:]
	}
	if len(fields) == 3 {
		return fields[1]
	}
	return defaultName(p, host)
}

// addImport adds an import line to the file, once.
func (e *editor) addImport(file, line string) {
	if e.imports == nil {
		e.imports = map[string][]string{}
	}
	for _, l := range e.imports[file] {
		if l == line {
			return
		}
	}
	e.imports[file] = append(e.imports[file], line)
	_, f := e.read(file)
	at := 0
	text := line + "\n"
	last := -1
	for _, d := range f.Decls {
		if _, ok := d.(*syntax.ImportDecl); ok {
			last = int(d.End())
		}
	}
	if last >= 0 {
		at, text = last, "\n"+line
	} else if len(f.Decls) > 0 {
		at = int(f.Decls[0].Pos())
		if td, ok := f.Decls[0].(*syntax.TypeDecl); ok && td.Doc != nil {
			at = int(td.Doc.Pos())
		}
		if fd, ok := f.Decls[0].(*syntax.FuncDecl); ok && fd.Doc != nil {
			at = int(fd.Doc.Pos())
		}
		text += "\n"
	}
	e.add(file, edit{from: at, to: at, text: text})
}

// apply edits the file's text, last edit first, and formats it.
func (e *editor) apply(file string) (string, error) {
	src := e.texts[file]
	eds := e.edits[file]
	sort.SliceStable(eds, func(i, j int) bool {
		if eds[i].from != eds[j].from {
			return eds[i].from > eds[j].from
		}
		return eds[i].seq > eds[j].seq
	})
	orig := src
	for _, ed := range eds {
		src = src[:ed.from] + ed.text + src[ed.to:]
	}
	if canon, err := format.Source(orig); err != nil || canon != orig {
		// A file kept in a layout of its own (a formatter fixture) keeps
		// it: only the edit's text is new.
		if _, err := syntax.Parse(src); err != nil {
			return "", fmt.Errorf("the fixed text does not parse: %w\n%s", err, src)
		}
		return src, nil
	}
	out, err := format.Source(src)
	if err != nil {
		return "", fmt.Errorf("the fixed text does not parse: %w\n%s", err, src)
	}
	return out, nil
}
