package rpcgen

// ergo gen schema (wf10 JS0): a wire type's JSON schema as a constant of
// std jsonschema, written by the mapper the hub's Schema uses for an
// operation's arguments, so a tool's arguments, a model's answer format
// and a type's own schema are one dialect.

import (
	"fmt"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
)

// SchemaDir is the folder of the schema package of the package named
// pkgName: <pkg>schema, beside the hub's <pkg>api.
func SchemaDir(pkgName string) string { return pkgName + "schema" }

// TypeSchema is the JSON schema text of the wire form of the type typeName
// that pkg declares (docs/spec/types.md, Codecs 2): a record is an object
// whose keys are its fields' wire keys, a json.Optional field not
// required; a union is anyOf its variants, each with its "_tag" const.
func TypeSchema(pkg *check.Package, typeName string) (string, error) {
	n, err := wireNamed(pkg, typeName)
	if err != nil {
		return "", err
	}
	g := &hubGen{pkg: pkg, path: pkg.Path, op: typeName}
	s := g.named(n, map[*check.Named]bool{}, "value")
	if g.err != nil {
		return "", g.err
	}
	return s, nil
}

// ArgumentSchema is the JSON schema text of op's arguments, an operation
// of the operation type opName of pkg: the text the hub's Schema(op)
// holds, so `ergo gen llm` writes each tool's input as the hub does.
func ArgumentSchema(pkg *check.Package, path, opName string, op Op) (string, error) {
	g := &hubGen{pkg: pkg, path: path, op: opName}
	s := g.arguments(op)
	if g.err != nil {
		return "", g.err
	}
	return s, nil
}

// TypeExpr is t written in the source of a package generated beside the
// hub of pkg (whose import path is path), with the imports it names, by
// path: what the hub writes for a parameter's type.
func TypeExpr(pkg *check.Package, path string, t check.Type) (string, map[string]string, error) {
	g := &hubGen{pkg: pkg, path: path, imports: map[string]string{}}
	s := g.expr(t)
	if g.err != nil {
		return "", nil, g.err
	}
	return s, g.imports, nil
}

// Lit is s as an Ergo string literal, as the hub writes one.
func Lit(s string) string { return lit(s) }

// wireNamed is the declared, non-generic type typeName of pkg.
func wireNamed(pkg *check.Package, typeName string) (*check.Named, error) {
	for _, t := range pkg.Types {
		if t.Name != typeName {
			continue
		}
		n, ok := t.Type.(*check.Named)
		if !ok {
			return nil, fmt.Errorf("%s is not a declared type", typeName)
		}
		if len(n.TParams) > 0 {
			return nil, fmt.Errorf("%s is generic; ergo gen schema writes the schema of a type with no parameters", typeName)
		}
		if !exported(typeName) {
			return nil, fmt.Errorf("%s is unexported; the schema package names it from outside its package", typeName)
		}
		if _, ok := n.Underlying().(*check.Interface); ok {
			return nil, fmt.Errorf("%s is an operation type, which has no wire form; ergo gen api writes its arguments' schemas", typeName)
		}
		return n, nil
	}
	return nil, fmt.Errorf("package %s declares no type %s", pkg.Path, typeName)
}

// GenerateSchema writes the schema file of typeName, a type of pkg whose
// import path is path: DIR/<pkg>schema/<type>.ergo, which holds the
// function <Type>(), the schema as a jsonschema.Schema parsed once, and
// the law <Type>ValuesFitTheSchema: the JSON the derived codec writes for
// any value of the type fits it.
func GenerateSchema(pkg *check.Package, path, typeName string) (Files, error) {
	text, err := TypeSchema(pkg, typeName)
	if err != nil {
		return Files{}, err
	}
	pkgName := lastElem(path)
	q := pkgName + "." + typeName
	lower := strings.ToLower(typeName[:1]) + typeName[1:]
	var b strings.Builder
	fmt.Fprintf(&b, "import json\nimport jsonschema\nimport %s\n\n", path)
	fmt.Fprintf(&b, "// %s is the JSON schema of %s's wire form: the JSON its derived\n// codec writes and reads.\nfunc %s() jsonschema.Schema { %sSchema }\n\n", typeName, q, typeName, lower)
	fmt.Fprintf(&b, "// %sSchema is %s's schema, parsed once.\nlet %sSchema = parse%s()\n\n", lower, typeName, lower, typeName)
	fmt.Fprintf(&b, "// parse%s is the schema ergo gen schema wrote for %s.\nfunc parse%s() jsonschema.Schema {\n\tjsonschema.Parse(%s) catch {\n\tcase jsonschema.Invalid:\n\t\tUnreachable(\"ergo gen schema writes a schema\")\n\t}\n}\n\n", typeName, q, typeName, lit(text))
	fmt.Fprintf(&b, "// %sValuesFitTheSchema: the JSON the codec writes for any %s fits\n// %s().\nlaw %sValuesFitTheSchema(v %s) {\n\tlet written = json.Decode(json.Codec[json.Value]{MaxBytes: 0, MaxDepth: 0}, json.Encode(json.Codec[%s]{MaxBytes: 0, MaxDepth: 0}, v)) catch {\n\tcase _:\n\t\treturn false\n\t}\n\tjsonschema.Conforms(%s(), written)\n}\n", typeName, q, typeName, typeName, q, q, typeName)
	out, err := Source(b.String())
	if err != nil {
		return Files{}, fmt.Errorf("schemagen: %s.ergo does not format: %v\n%s", lower, err, b.String())
	}
	return Files{Dir: SchemaDir(pkgName), Files: map[string]string{strings.ToLower(typeName) + ".ergo": out}, Marker: Marker("schema", path+"."+typeName, "")}, nil
}
