package tsproj

import (
	"fmt"
	"go/constant"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/rpcgen"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

type httpTable struct {
	name   string
	pos    syntax.Pos
	local  bool
	routes []httpRoute
}

type httpRoute struct {
	name, method, path string
	status             int
	params             []*check.Var
	payload            *check.Var
	result             check.Type
	failures           []httpFailure
}

type httpFailure struct {
	typ    check.Type
	status int
}

// One table in the package serves all its HTTP contracts. Tags are the codec's
// nominal failure names, not expressions evaluated by the TypeScript peer.
func httpFailureStatuses(pkg *check.Package) (map[string]int, error) {
	var statuses map[string]int
	for _, init := range pkg.Info.InitOrder {
		if len(init.Vars) != 1 || !public(init.Vars[0].Name) {
			continue
		}
		s, ok := init.Vars[0].Type.(*check.Slice)
		if !ok {
			continue
		}
		n, ok := s.Elem.(*check.Named)
		if !ok || n.Obj.Pkg == nil || !n.Obj.Pkg.Std() || n.Obj.Pkg.Path != "http" || n.Obj.Name != "FailureStatus" {
			continue
		}
		bad := func(why string) error { return fmt.Errorf("HTTP failure table %s: %s", init.Vars[0].Name, why) }
		if statuses != nil {
			return nil, bad("needs only one exported failure table per package")
		}
		statuses = map[string]int{}
		lit, ok := init.Value.(*syntax.CompositeLit)
		if !ok || lit.Base != nil {
			return nil, bad("requires a constant literal slice of literal failure statuses")
		}
		for _, e := range lit.Elts {
			r, ok := e.(*syntax.CompositeLit)
			if !ok || r.Base != nil {
				return nil, bad("requires literal failure statuses")
			}
			fields := map[string]syntax.Expr{}
			for _, e := range r.Elts {
				kv, ok := e.(*syntax.KeyValueExpr)
				if !ok {
					return nil, bad("requires named literal fields")
				}
				id, ok := kv.Key.(*syntax.Ident)
				if !ok {
					return nil, bad("requires named literal fields")
				}
				fields[id.Name] = kv.Value
			}
			tag, ok := stringLiteral(fields["Tag"])
			l, literal := fields["Status"].(*syntax.BasicLit)
			if !ok || tag == "" || !literal || l.Kind != syntax.INT {
				return nil, bad("Tag and Status must be literals")
			}
			status, exact := constant.Int64Val(pkg.Info.Values[l])
			if !exact || status < 400 || status > 599 {
				return nil, bad("Status must be a failure status (400..599)")
			}
			if _, exists := statuses[tag]; exists {
				return nil, bad("repeated failure " + tag)
			}
			statuses[tag] = int(status)
		}
	}
	return statuses, nil
}

func httpFailures(t check.Type) []*check.Named {
	if n, ok := t.(*check.Named); ok {
		if u, ok := n.Underlying().(*check.Union); ok {
			var all []*check.Named
			for _, v := range u.Variants {
				all = append(all, httpFailures(v)...)
			}
			return all
		}
		return []*check.Named{n}
	}
	return nil
}

// HTTP tables are data, not evaluated Ergo code. Only the projection reads
// this finite profile; it adds no language checking or runtime semantics.
func httpTables(pkg *check.Package, file *syntax.File, contracts []*check.TypeName) ([]httpTable, error) {
	statuses, err := httpFailureStatuses(pkg)
	if err != nil {
		return nil, err
	}
	failureTypes := map[string]*check.Named{}
	local := map[*syntax.Ident]bool{}
	for _, d := range file.Decls {
		if v, ok := d.(*syntax.VarDecl); ok {
			for _, s := range v.Specs {
				for _, id := range s.Names {
					local[id] = true
				}
			}
		}
	}
	var tables []httpTable
	for _, init := range pkg.Info.InitOrder {
		if len(init.Vars) != 1 || !public(init.Vars[0].Name) {
			continue
		}
		v := init.Vars[0]
		s, ok := v.Type.(*check.Slice)
		if !ok {
			continue
		}
		r, ok := s.Elem.(*check.Named)
		if !ok || r.Obj.Pkg == nil || !r.Obj.Pkg.Std() || r.Obj.Pkg.Path != "http" || r.Obj.Name != "Route" || len(r.TArgs) != 1 {
			continue
		}
		errTable := func(why string) error { return fmt.Errorf("HTTP table %s: %s", v.Name, why) }
		endpoint, ok := r.TArgs[0].(*check.Named)
		if !ok {
			return nil, errTable("Route's endpoint must be a closed union")
		}
		u, ok := endpoint.Underlying().(*check.Union)
		if !ok || len(u.Variants) == 0 {
			return nil, errTable("Route's endpoint must be a closed union")
		}
		variants := map[check.Type]string{}
		for _, t := range u.Variants {
			n, ok := t.(*check.Named)
			if !ok {
				return nil, errTable("endpoints must be named empty variants")
			}
			st, ok := n.Underlying().(*check.Struct)
			if !ok || len(st.Fields) != 0 {
				return nil, errTable("endpoints must be named empty variants")
			}
			variants[t] = n.Obj.Name
		}
		var ops []rpcgen.Op
		group := ""
		for _, c := range contracts {
			candidate, err := rpcgen.Ops(pkg, c.Name)
			if err != nil {
				return nil, err
			}
			if len(candidate) != len(variants) {
				continue
			}
			names := map[string]bool{}
			for _, op := range candidate {
				names[op.Name] = true
			}
			match := true
			for _, name := range variants {
				match = match && names[name]
			}
			if !match {
				continue
			}
			if group != "" {
				return nil, errTable("more than one exported operation type names its endpoints")
			}
			group, ops = c.Name, candidate
		}
		if group == "" {
			return nil, errTable("needs one exported operation type naming every endpoint")
		}
		lit, ok := init.Value.(*syntax.CompositeLit)
		if !ok || lit.Base != nil {
			return nil, errTable("requires a constant literal slice of literal routes")
		}
		table := httpTable{name: v.Name, pos: v.Pos}
		for id := range local {
			if pkg.Info.Defs[id] == v {
				table.local = true
			}
		}
		seen := map[string]bool{}
		patterns := map[string]bool{}
		for _, e := range lit.Elts {
			record, ok := e.(*syntax.CompositeLit)
			if !ok || record.Base != nil {
				return nil, errTable("requires a constant literal slice of literal routes")
			}
			fields := map[string]syntax.Expr{}
			for _, e := range record.Elts {
				kv, ok := e.(*syntax.KeyValueExpr)
				if !ok {
					return nil, errTable("routes require named literal fields")
				}
				id, ok := kv.Key.(*syntax.Ident)
				if !ok {
					return nil, errTable("routes require named literal fields")
				}
				fields[id.Name] = kv.Value
			}
			name := variants[pkg.Info.Types[fields["Value"]]]
			if name == "" {
				return nil, errTable("Value must name an endpoint variant")
			}
			switch value := fields["Value"].(type) {
			case *syntax.Ident:
				if _, ok := pkg.Info.Uses[value].(*check.TypeName); !ok {
					return nil, errTable("Value must name an endpoint variant")
				}
			case *syntax.SelectorExpr:
				id, _ := value.X.(*syntax.Ident)
				pn, _ := pkg.Info.Uses[id].(*check.PkgName)
				n, _ := pkg.Info.Types[value].(*check.Named)
				if pn == nil || n == nil || pn.Pkg != n.Obj.Pkg || value.Sel.Name != n.Obj.Name {
					return nil, errTable("Value must name an endpoint variant")
				}
			case *syntax.CompositeLit:
				if value.Base != nil || len(value.Elts) != 0 {
					return nil, errTable("Value must name an endpoint variant")
				}
			default:
				return nil, errTable("Value must name an endpoint variant")
			}
			if seen[name] {
				return nil, errTable("must route every endpoint exactly once: repeated " + name)
			}
			seen[name] = true
			method, mOK := stringLiteral(fields["Method"])
			path, pOK := stringLiteral(fields["Path"])
			statusLit, sOK := fields["Status"].(*syntax.BasicLit)
			if !mOK || !pOK || !sOK || statusLit.Kind != syntax.INT {
				return nil, errTable("Method, Path and Status must be literals")
			}
			status, exact := constant.Int64Val(pkg.Info.Values[statusLit])
			if !exact || status < 200 || status > 299 {
				return nil, errTable("Status must be a success status (200..299)")
			}
			switch method {
			case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
			default:
				return nil, errTable("unsupported HTTP method " + method)
			}
			params, pattern, err := pathParameters(path)
			if err != nil {
				return nil, errTable(err.Error())
			}
			key := method + " " + pattern
			if patterns[key] {
				return nil, errTable("repeated method/path pattern " + key)
			}
			patterns[key] = true
			var op rpcgen.Op
			for _, candidate := range ops {
				if candidate.Name == name {
					op = candidate
				}
			}
			route := httpRoute{name: name, method: method, path: path, status: int(status), result: op.Result}
			for _, f := range op.Fails {
				for _, n := range httpFailures(f) {
					tag := n.Obj.Name
					if prior := failureTypes[tag]; prior != nil && prior != n {
						return nil, errTable("distinct failures share the tag " + tag)
					}
					failureTypes[tag] = n
					status, ok := statuses[tag]
					if !ok {
						return nil, errTable("missing status for failure " + tag)
					}
					route.failures = append(route.failures, httpFailure{typ: n, status: status})
				}
			}
			for _, p := range op.Params {
				if params[p.Name] {
					if _, ok := p.Type.(*check.Basic); !ok {
						return nil, errTable(name + ": path parameters must be scalar")
					}
					route.params = append(route.params, p)
					delete(params, p.Name)
				} else {
					if route.payload != nil {
						return nil, errTable(name + ": at most one JSON payload parameter")
					}
					route.payload = p
				}
			}
			if len(params) != 0 {
				return nil, errTable(name + ": every path parameter needs an operation parameter of that name")
			}
			if route.payload != nil && method != "POST" && method != "PUT" && method != "PATCH" && method != "DELETE" {
				return nil, errTable(name + ": a JSON payload needs a body method")
			}
			if op.Result != nil && (status == 204 || status == 205 || method == "HEAD") {
				return nil, errTable(name + ": a no-content response cannot carry a result")
			}
			table.routes = append(table.routes, route)
		}
		if len(seen) != len(variants) {
			return nil, errTable("must route every endpoint exactly once: missing endpoint")
		}
		tables = append(tables, table)
	}
	if len(tables) > 0 {
		for tag := range statuses {
			if failureTypes[tag] == nil {
				return nil, fmt.Errorf("HTTP failure table: %s is not a contract failure", tag)
			}
		}
	}
	return tables, nil
}

func stringLiteral(e syntax.Expr) (string, bool) {
	l, ok := e.(*syntax.BasicLit)
	if !ok || l.Kind != syntax.STRING {
		return "", false
	}
	s, err := strconv.Unquote(l.Value)
	return s, err == nil
}

func pathParameters(path string) (map[string]bool, string, error) {
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "?#*") {
		return nil, "", fmt.Errorf("Path must be an absolute segment path without query, fragment or wildcard")
	}
	params := map[string]bool{}
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if !strings.Contains(part, ":") {
			continue
		}
		name := strings.TrimPrefix(part, ":")
		if name == part || name == "" || params[name] || strings.Contains(name, ":") {
			return nil, "", fmt.Errorf("Path needs distinct whole-segment :parameters")
		}
		params[name] = true
		parts[i] = ":"
	}
	return params, strings.Join(parts, "/"), nil
}

func (g *emitter) httpAPI(table httpTable) {
	// Resolve schemas before opening the API expression: declarations are
	// emitted by schema, and must not interrupt an endpoint's expression.
	for _, route := range table.routes {
		for _, p := range route.params {
			g.schema(p.Type)
		}
		if route.payload != nil {
			g.schema(route.payload.Type)
		}
		if route.result != nil {
			g.schema(route.result)
		}
		for _, f := range route.failures {
			g.schema(f.typ)
		}
	}
	g.s("export const ")
	g.name(table.name, table.name, table.pos, table.local)
	g.s(" = $HttpApi.make(" + strconv.Quote(table.name) + ").add(\n")
	// Grouping by method keeps a method-only drift visible at the call site:
	// client.GET.Read no longer exists when Read moves to POST. A flat group
	// loses that fact when the request and response shapes stay unchanged.
	var methods []string
	for _, route := range table.routes {
		found := false
		for _, method := range methods {
			found = found || method == route.method
		}
		if !found {
			methods = append(methods, route.method)
		}
	}
	for _, method := range methods {
		g.s("$HttpApiGroup.make(" + strconv.Quote(method) + ").add(\n")
		for _, route := range table.routes {
			if route.method != method {
				continue
			}
			g.s("$HttpApiEndpoint.make(" + strconv.Quote(route.method) + ")(" + strconv.Quote(route.name) + ", " + strconv.Quote(route.path) + ", { success: ")
			if route.result == nil {
				g.s("$HttpApiSchema.Empty(" + strconv.Itoa(route.status) + ")")
			} else {
				g.s(g.schema(route.result) + ".pipe($HttpApiSchema.status(" + strconv.Itoa(route.status) + "))")
			}
			if len(route.params) > 0 {
				g.s(", params: { ")
				for _, p := range route.params {
					g.s(strconv.Quote(p.Name) + ": " + g.schema(p.Type) + ", ")
				}
				g.s("}")
			}
			if route.payload != nil {
				g.s(", payload: " + g.schema(route.payload.Type))
			}
			if len(route.failures) > 0 {
				g.s(", error: [")
				for _, f := range route.failures {
					g.s(g.errorSchema(f.typ) + ".pipe($HttpApiSchema.status(" + strconv.Itoa(f.status) + ")), ")
				}
				g.s("]")
			}
			g.s(" }),\n")
		}
		g.s("),\n")
	}
	g.s(");\n")
}
