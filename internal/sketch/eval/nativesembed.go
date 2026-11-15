package eval

import (
	"io/fs"
	"testing/fstest"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// embedded[T](pattern) (ORACLE O15; stdlib.md, embed): the checker's
// asset snapshot (Info.Calls' Embed), never the host's files. A string or
// a []byte is the one file's bytes; an embed.FS is a read-only tree with
// io/fs's rules, as Go's embed.FS gives them to the build: a file's mode
// 0444, a directory's 0555, ReadDir's names sorted, and every failure
// fs.NotFound with the name asked for.
func (in *interp) embedValue(fr *frame, x *syntax.CallExpr, c *check.Call) (Value, *abrupt) {
	e := c.Embed
	if e == nil || len(e.Files) == 0 {
		ice("embedded with no checked asset snapshot")
	}
	size := int64(0)
	for _, f := range e.Files {
		size += int64(len(f.Data))
	}
	if ab := in.build(fr, x, 1+int64(len(e.Files)), 0, size); ab != nil {
		return nil, ab
	}
	t := fr.rtypeOf(x)
	if !e.Tree {
		data := string(e.Files[0].Data)
		if _, ok := t.under().t.(*check.Slice); ok {
			return bytesValue(data), nil
		}
		return data, nil
	}
	n, ok := t.t.(*check.Named)
	if !ok {
		ice("an embedded tree of type %s", t.t)
	}
	tree := newEmbedFS(n)
	for _, f := range e.Files {
		tree.files[f.Path] = &fstest.MapFile{Data: f.Data, Mode: 0o444}
	}
	return tree, nil
}

// newEmbedFS is an empty tree of the embed.FS type n.
func newEmbedFS(n *check.Named) *embedFS {
	tree := &embedFS{files: fstest.MapFS{}}
	iface, ok := n.Underlying().(*check.Interface)
	if !ok {
		ice("embed.FS is not an operation type")
	}
	for _, m := range iface.Methods {
		if m.Sig.Errors != nil {
			for _, el := range m.Sig.Errors.Elems() {
				if nf, ok := el.(*check.Named); ok && nf.Obj.Name == "NotFound" {
					tree.notFound = nf
				}
			}
		}
		if m.Name == "Stat" && len(m.Sig.Results) == 1 {
			tree.info, _ = m.Sig.Results[0].(*check.Named)
		}
	}
	if tree.notFound == nil || tree.info == nil {
		ice("embed.FS's operations name no fs.NotFound or fs.Info")
	}
	return tree
}

func init() {
	natives["embed.NewEmpty"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		if ab := in.charge(fr, at, 1); ab != nil {
			return nil, ab
		}
		n, ok := fn.Sig.Results[0].(*check.Named)
		if !ok {
			ice("embed.NewEmpty gives %s", fn.Sig.Results[0])
		}
		return newEmbedFS(n), nil
	}
}

// bytesValue is the []byte of s's bytes.
func bytesValue(s string) Value {
	vs := make([]Value, len(s))
	for k := 0; k < len(s); k++ {
		vs[k] = s[k]
	}
	return newSlice(vs)
}

// embedFS is an embed.FS value: the tree, and the fs types its operations
// give.
type embedFS struct {
	files    fstest.MapFS
	notFound *check.Named
	info     *check.Named
}

func (f *embedFS) describe() string { return "an embed.FS" }

func (f *embedFS) opCall(in *interp, fr *frame, at syntax.Node, name string, args []Value) (Value, *abrupt) {
	if ab := in.charge(fr, at, 1); ab != nil {
		return nil, ab
	}
	p := args[0].(string)
	missing := func() *abrupt {
		r := &record{t: f.notFound, fields: make([]Value, len(structOf(f.notFound).Fields))}
		r.fields[fieldIndex(f.notFound, "Path")] = p
		return fr.abrupt(ctlFail, at, r, "")
	}
	switch name {
	case "ReadFile":
		data, err := fs.ReadFile(f.files, p)
		if err != nil {
			return nil, missing()
		}
		if ab := in.build(fr, at, 1, 0, int64(len(data))); ab != nil {
			return nil, ab
		}
		return string(data), nil
	case "ReadDir":
		entries, err := fs.ReadDir(f.files, p)
		if err != nil {
			return nil, missing()
		}
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		return stringSlice(names), nil
	case "Stat":
		st, err := fs.Stat(f.files, p)
		if err != nil {
			return nil, missing()
		}
		r := &record{t: f.info, fields: make([]Value, len(structOf(f.info).Fields))}
		for k, v := range map[string]Value{"Name": st.Name(), "Size": st.Size(), "Dir": st.IsDir(), "Perm": int64(st.Mode().Perm())} {
			r.fields[fieldIndex(f.info, k)] = v
		}
		return r, nil
	}
	return nil, fr.unsupported(at, "the embed.FS operation %s", name)
}
