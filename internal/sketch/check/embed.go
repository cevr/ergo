package check

import (
	"crypto/sha256"
	"fmt"
	"go/constant"

	"ergo.local/prototype/internal/sketch/syntax"
)

// EmbedFile is one checked build input, relative to its declaring package.
// The loader reads it once; lowering never re-opens the owner's files.
type EmbedFile struct {
	Path string
	Data []byte
}

// Embed is a compile-time asset snapshot. Prefix isolates package assets in
// the flattened Go module. Tree retains package-relative names inside FS.
type Embed struct {
	Prefix string
	Tree   bool
	Files  []EmbedFile
}

// EmbedImporter extends Importer for source packages with build-time assets.
// A checker without this boundary cannot read host files implicitly.
type EmbedImporter interface {
	ResolveEmbed(file *syntax.File, pattern string, tree bool) ([]EmbedFile, error)
}

func (c *checker) embedCall(x *syntax.CallExpr, t Type) operand {
	if t == nil || isInvalid(t) || len(x.Args) != 1 {
		c.errorf(x, CodeType, "embedded takes one type and one path pattern: embedded[string](\"help.txt\")")
		c.silentArgs(x)
		return bad(x)
	}
	var spec *syntax.VarSpec
	for _, d := range c.cur.syntax.Decls {
		if d, ok := d.(*syntax.VarDecl); ok && !d.Mut.IsValid() {
			for _, s := range d.Specs {
				if len(s.Names) == 1 && s.Names[0].Name != "_" && len(s.Values) == 1 && s.Values[0] == x {
					spec = s
				}
			}
		}
	}
	if spec == nil {
		c.errorf(x, CodeType, "embed stands only as the whole initializer of an immutable package let")
		c.silentArgs(x)
		return bad(x)
	}
	tree := false
	if n, ok := t.(*Named); ok && n.Obj.Pkg != nil && n.Obj.Pkg.Std() && n.Obj.Pkg.Path == "embed" && n.Obj.Name == "FS" {
		tree = true
	}
	bytes := false
	if s, ok := t.(*Slice); ok {
		bytes = c.identical(s.Elem, Typ[Uint8])
	}
	if t != Typ[String] && !bytes && !tree {
		c.errorf(x, CodeType, "embed's type is string, []byte or embed.FS, not %s", t)
		c.silentArgs(x)
		return bad(x)
	}
	p := c.valueExpr(x.Args[0], Typ[String])
	if p.invalid() {
		return bad(x)
	}
	if p.mode != modeConst || p.val.Kind() != constant.String {
		c.errorf(x.Args[0], CodeType, "embed's path pattern is a constant string")
		return bad(x)
	}
	resolver, ok := c.imp.(EmbedImporter)
	if !ok {
		c.errorf(x, CodeType, "embed needs a source package with a build-time asset loader")
		return bad(x)
	}
	files, err := resolver.ResolveEmbed(c.cur.syntax, constant.StringVal(p.val), tree)
	if err != nil {
		c.errorf(x.Args[0], CodeType, "embed: %v", err)
		return bad(x)
	}
	key := fmt.Sprintf("%s/%s:%d", c.pkg.Path, c.cur.syntax.Name, x.Pos())
	sum := sha256.Sum256([]byte(key))
	call := c.callInfo(x)
	call.Kind, call.Type = CallEmbed, t
	call.Embed = &Embed{Prefix: fmt.Sprintf("assets/%x", sum[:12]), Tree: tree, Files: files}
	return operand{mode: modeValue, typ: t}
}
