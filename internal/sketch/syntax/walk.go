package syntax

// Inspect traverses the tree rooted at n in depth-first order, as
// go/ast.Inspect does: it calls f(n), and when f returns true, inspects
// each child. Comments are not visited.
func Inspect(n Node, f func(Node) bool) {
	if n == nil || isNilNode(n) || !f(n) {
		return
	}
	walk := func(children ...Node) {
		for _, c := range children {
			if c != nil && !isNilNode(c) {
				Inspect(c, f)
			}
		}
	}
	exprs := func(list []Expr) {
		for _, x := range list {
			Inspect(x, f)
		}
	}
	stmts := func(list []Stmt) {
		for _, s := range list {
			Inspect(s, f)
		}
	}
	idents := func(list []*Ident) {
		for _, x := range list {
			Inspect(x, f)
		}
	}
	fields := func(l *FieldList) {
		if l == nil {
			return
		}
		for _, x := range l.List {
			Inspect(x, f)
		}
	}
	switch n := n.(type) {
	case *File:
		for _, d := range n.Decls {
			Inspect(d, f)
		}
	case *ImportDecl:
		for _, s := range n.Specs {
			Inspect(s, f)
		}
	case *ImportSpec:
		walk(n.Name, n.Path)
	case *VarDecl:
		walk(n.Extern)
		for _, s := range n.Specs {
			Inspect(s, f)
		}
	case *VarSpec:
		idents(n.Names)
		walk(n.Pattern, n.Type)
		exprs(n.Values)
	case *TypeDecl:
		walk(n.Extern, n.Name, n.TypeParams, n.Spec, n.Where)
	case *FuncDecl:
		walk(n.Extern, n.Recv, n.Name, n.TypeParams, n.Type, n.Body)
	case *ExternSpec:
		walk(n.Target)
	case *TestDecl:
		walk(n.Name, n.Params, n.Body)
	case *LawDecl:
		walk(n.Name, n.Params, n.Body)
		if n.Reach != nil {
			idents(n.Reach.Targets)
		}
	case *AssertStmt:
		walk(n.X)
	case *VariantSpec:
		walk(n.Name, n.TypeArgs, n.Fields)
	case *AsPattern:
		walk(n.X, n.Name)
	case *InterpolatedString:
		for i, t := range n.Texts {
			Inspect(t, f)
			if i < len(n.Exprs) {
				Inspect(n.Exprs[i], f)
			}
		}
	case *ContextDecl:
		walk(n.Name, n.Provides, n.Needs, n.Body)
	case *ContextBody:
		for _, e := range n.Entries {
			Inspect(e, f)
		}
	case *ContextEntry:
		walk(n.Key)
		exprs(n.Keys)
		walk(n.Value)
	case *Field:
		idents(n.Names)
		walk(n.Type, n.Key, n.Where)
	case *FieldList:
		fields(n)
	case *TakesClause:
		// The names refer to the signature's parameters: not visited.
	case *NeedsClause:
		exprs(n.Names)
	case *ProvidesClause:
		exprs(n.Names)
	case *CompositeLit:
		walk(n.Type, n.Base)
		exprs(n.Elts)
	case *KeyValueExpr:
		walk(n.Key, n.Value)
	case *FuncLit:
		walk(n.Type, n.Body)
	case *ParenExpr:
		walk(n.X)
	case *SelectorExpr:
		walk(n.X, n.Sel)
	case *IndexExpr:
		walk(n.X)
		exprs(n.Indices)
	case *SliceExpr:
		walk(n.X, n.Low, n.High, n.Max)
	case *TypeGuard:
		walk(n.X)
	case *CallExpr:
		walk(n.Fun)
		exprs(n.Args)
		walk(n.Trailing)
	case *StarExpr:
		walk(n.X)
	case *UnaryExpr:
		walk(n.X)
	case *BinaryExpr:
		walk(n.X, n.Y)
	case *TryExpr:
		walk(n.X)
	case *CatchExpr:
		walk(n.X)
		for _, a := range n.Arms {
			Inspect(a, f)
		}
	case *CatchArm:
		exprs(n.Types)
		stmts(n.Body)
	case *ContextLit:
		walk(n.Provides, n.Needs, n.Body)
	case *ArrayType:
		walk(n.Elt)
	case *MapType:
		walk(n.Key, n.Value)
	case *FuncType:
		walk(n.Params, n.Results, n.Needs, n.Takes)
	case *StructType:
		walk(n.Fields)
	case *InterfaceType:
		for _, m := range n.Methods {
			Inspect(m, f)
		}
		for _, l := range n.Laws {
			Inspect(l, f)
		}
		exprs(n.Union)
	case *ErrorRow:
		exprs(n.Types)
	case *MachineType:
		walk(n.State, n.Signal)
		for _, e := range n.Entries {
			Inspect(e, f)
		}
		for _, l := range n.Laws {
			Inspect(l, f)
		}
	case *MachineEntry:
		walk(n.Value)
	case *TupleExpr:
		exprs(n.Elts)
	case *DeclStmt:
		walk(n.Decl)
	case *ExprStmt:
		walk(n.X)
	case *AssignStmt:
		exprs(n.Lhs)
		exprs(n.Rhs)
	case *ReturnStmt:
		exprs(n.Results)
	case *BlockStmt:
		stmts(n.List)
	case *IfStmt:
		walk(n.Cond, n.Body, n.Else)
	case *CaseClause:
		exprs(n.List)
		stmts(n.Body)
	case *SwitchStmt:
		walk(n.Tag)
		for _, c := range n.Clauses {
			Inspect(c, f)
		}
	case *TypeSwitchStmt:
		for _, g := range n.Subjects {
			Inspect(g, f)
		}
		for _, c := range n.Clauses {
			Inspect(c, f)
		}
	case *ForStmt:
		walk(n.Cond, n.Body)
	case *RangeStmt:
		walk(n.Key, n.Value, n.X, n.Body)
	}
}

// isNilNode reports whether n is a typed nil pointer inside the interface.
func isNilNode(n Node) bool {
	switch n := n.(type) {
	case *Ident:
		return n == nil
	case *BasicLit:
		return n == nil
	case *FieldList:
		return n == nil
	case *TakesClause:
		return n == nil
	case *NeedsClause:
		return n == nil
	case *ProvidesClause:
		return n == nil
	case *BlockStmt:
		return n == nil
	case *FuncType:
		return n == nil
	case *CallExpr:
		return n == nil
	case *ContextBody:
		return n == nil
	case *VarDecl:
		return n == nil
	case *ExternSpec:
		return n == nil
	case *LawDecl:
		return n == nil
	case *StructType:
		return n == nil
	}
	return false
}
