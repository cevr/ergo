package rpcgen

// ergo gen schema (wf10 JS0): a wire type's JSON schema as a constant of
// std jsonschema, written by the mapper the hub's Schema uses for an
// operation's arguments, so a tool's arguments, a model's answer format
// and a type's own schema are one dialect.

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
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

// SchemaExpr converts a JSON Schema document produced by TypeSchema or
// ArgumentSchema into a typed Ergo std/jsonschema expression, preserving
// property key order and failing at generation time if the JSON is malformed
// or outside the emitted subset.
func SchemaExpr(text string) (string, error) {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	val, err := decodeOrderedJSON(dec)
	if err != nil {
		return "", fmt.Errorf("schema json: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return "", fmt.Errorf("schema json: trailing tokens")
	}
	return orderedJSONToSchemaExpr(val)
}

type orderedMember struct {
	key string
	val any
}

type orderedObject []orderedMember

func (o orderedObject) get(key string) (any, bool) {
	for _, m := range o {
		if m.key == key {
			return m.val, true
		}
	}
	return nil, false
}

func decodeOrderedJSON(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	d, ok := tok.(json.Delim)
	if !ok {
		return tok, nil
	}
	switch d {
	case '{':
		var out orderedObject
		for dec.More() {
			k, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, ok := k.(string)
			if !ok {
				return nil, fmt.Errorf("expected object key, got %T", k)
			}
			v, err := decodeOrderedJSON(dec)
			if err != nil {
				return nil, err
			}
			out = append(out, orderedMember{key: key, val: v})
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return out, nil
	case '[':
		var out []any
		for dec.More() {
			v, err := decodeOrderedJSON(dec)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unexpected delimiter %q", d)
	}
}

func orderedJSONToSchemaExpr(v any) (string, error) {
	obj, ok := v.(orderedObject)
	if !ok {
		return "", fmt.Errorf("schema must be a JSON object, got %T", v)
	}
	if len(obj) == 0 {
		return "jsonschema.Any()", nil
	}
	if raw, ok := obj.get("const"); ok && len(obj) == 1 {
		s, ok := raw.(string)
		if !ok {
			return "", fmt.Errorf("const must be a string, got %T", raw)
		}
		return fmt.Sprintf("jsonschema.ConstString(%s)", lit(s)), nil
	}
	if raw, ok := obj.get("anyOf"); ok && len(obj) == 1 {
		arr, ok := raw.([]any)
		if !ok || len(arr) == 0 {
			return "", fmt.Errorf("anyOf must be a non-empty array")
		}
		first, err := orderedJSONToSchemaExpr(arr[0])
		if err != nil {
			return "", err
		}
		var rest []string
		for _, item := range arr[1:] {
			e, err := orderedJSONToSchemaExpr(item)
			if err != nil {
				return "", err
			}
			rest = append(rest, e)
		}
		return fmt.Sprintf("jsonschema.AnyOf(%s, []jsonschema.Schema{%s})", first, strings.Join(rest, ", ")), nil
	}
	typVal, ok := obj.get("type")
	if !ok {
		return "", fmt.Errorf("unsupported schema object keys")
	}
	typ, ok := typVal.(string)
	if !ok {
		return "", fmt.Errorf("schema type must be a string, got %T", typVal)
	}
	switch typ {
	case "null":
		if len(obj) == 1 {
			return "jsonschema.Null()", nil
		}
	case "boolean":
		if len(obj) == 1 {
			return "jsonschema.Bool()", nil
		}
	case "string":
		if len(obj) == 1 {
			return "jsonschema.String()", nil
		}
	case "integer":
		if len(obj) == 1 {
			return "jsonschema.Integer()", nil
		}
	case "number":
		if len(obj) == 1 {
			return "jsonschema.Number()", nil
		}
	case "array":
		if rawItems, ok := obj.get("items"); ok {
			elem, err := orderedJSONToSchemaExpr(rawItems)
			if err != nil {
				return "", err
			}
			if len(obj) == 2 {
				return fmt.Sprintf("jsonschema.Array(%s)", elem), nil
			}
			minRaw, okMin := obj.get("minItems")
			maxRaw, okMax := obj.get("maxItems")
			if len(obj) == 4 && okMin && okMax {
				minN, err1 := jsonInt(minRaw)
				maxN, err2 := jsonInt(maxRaw)
				if err1 == nil && err2 == nil && minN == maxN && minN >= 0 {
					return fmt.Sprintf("jsonschema.FixedArray(%s, %d)", elem, minN), nil
				}
			}
		}
		if rawPrefix, ok := obj.get("prefixItems"); ok && len(obj) == 2 {
			arr, ok := rawPrefix.([]any)
			if !ok {
				return "", fmt.Errorf("prefixItems must be an array")
			}
			var elems []string
			for _, item := range arr {
				e, err := orderedJSONToSchemaExpr(item)
				if err != nil {
					return "", err
				}
				elems = append(elems, e)
			}
			return fmt.Sprintf("jsonschema.Tuple([]jsonschema.Schema{%s})", strings.Join(elems, ", ")), nil
		}
	case "object":
		if len(obj) == 1 {
			return "jsonschema.ObjectAny()", nil
		}
		if rawAdd, ok := obj.get("additionalProperties"); ok && len(obj) == 2 {
			add, err := orderedJSONToSchemaExpr(rawAdd)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("jsonschema.Record(%s)", add), nil
		}
		if rawProps, ok := obj.get("properties"); ok {
			propsObj, ok := rawProps.(orderedObject)
			if !ok {
				return "", fmt.Errorf("properties must be an object")
			}
			var props []string
			for _, m := range propsObj {
				e, err := orderedJSONToSchemaExpr(m.val)
				if err != nil {
					return "", err
				}
				props = append(props, fmt.Sprintf("jsonschema.Prop{Key: %s, Schema: %s}", lit(m.key), e))
			}
			var req []string
			if rawReq, hasReq := obj.get("required"); hasReq {
				if len(obj) != 3 {
					return "", fmt.Errorf("unexpected extra keys in object schema")
				}
				arr, ok := rawReq.([]any)
				if !ok {
					return "", fmt.Errorf("required must be an array")
				}
				for _, r := range arr {
					s, ok := r.(string)
					if !ok {
						return "", fmt.Errorf("required entry must be a string")
					}
					req = append(req, lit(s))
				}
			} else if len(obj) != 2 {
				return "", fmt.Errorf("unexpected extra keys in object schema")
			}
			return fmt.Sprintf("jsonschema.Object([]jsonschema.Prop{%s}, []string{%s})", strings.Join(props, ", "), strings.Join(req, ", ")), nil
		}
	}
	return "", fmt.Errorf("unsupported schema shape for type %q", typ)
}

func jsonInt(v any) (int, error) {
	num, ok := v.(json.Number)
	if !ok {
		return 0, fmt.Errorf("expected number, got %T", v)
	}
	return strconv.Atoi(num.String())
}

// GenerateSchema writes the schema file of typeName, a type of pkg whose
// import path is path: DIR/<pkg>schema/<type>.ergo, which holds the
// function <Type>(), the schema built as a typed jsonschema.Schema, and
// the law <Type>ValuesFitTheSchema: the JSON the derived codec writes for
// any value of the type fits it.
func GenerateSchema(pkg *check.Package, path, typeName string) (Files, error) {
	text, err := TypeSchema(pkg, typeName)
	if err != nil {
		return Files{}, err
	}
	expr, err := SchemaExpr(text)
	if err != nil {
		return Files{}, err
	}
	pkgName := lastElem(path)
	q := pkgName + "." + typeName
	lower := strings.ToLower(typeName[:1]) + typeName[1:]
	var b strings.Builder
	fmt.Fprintf(&b, "import json\nimport jsonschema\nimport %s\n\n", path)
	fmt.Fprintf(&b, "func %s() jsonschema.Schema { %sSchema }\n\n", typeName, lower)
	fmt.Fprintf(&b, "let %sSchema = %s\n\n", lower, expr)
	fmt.Fprintf(&b, "law %sValuesFitTheSchema(v %s) {\n\tlet written = json.Decode(json.Codec[json.Value]{MaxBytes: 0, MaxDepth: 0}, json.Encode(json.Codec[%s]{MaxBytes: 0, MaxDepth: 0}, v)) catch {\n\tcase _:\n\t\treturn false\n\t}\n\tjsonschema.Conforms(%s(), written)\n}\n", typeName, q, q, typeName)
	out, err := Source(b.String())
	if err != nil {
		return Files{}, fmt.Errorf("schemagen: %s.ergo does not format: %v\n%s", lower, err, b.String())
	}
	return Files{Dir: SchemaDir(pkgName), Files: map[string]string{strings.ToLower(typeName) + ".ergo": out}, Marker: Marker("schema", path+"."+typeName, "")}, nil
}
