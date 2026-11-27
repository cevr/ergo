package contentmap

import (
	"fmt"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/syntax"
)

func init() {
	check.RegisterDeriveExpander(ExpandInlineDerives)
}

type pendingDerive struct {
	index       int
	fileIdx     int
	file        *syntax.File
	decl        *syntax.TypeDecl
	target      string
	expr        syntax.Expr
	line        uint32
	col         uint32
	byteCol     int
	lineInt     int
	reachIdents map[string]bool
	done        bool
	refused     bool
}

type genFileContext struct {
	origFileIdx int
	origFile    *syntax.File
	deriveExpr  syntax.Expr
	target      string
	spans       []eval.MapperSpanMapping
	byteOrigins []eval.SourceByteOrigin
	companions  []eval.MapperCompanion
}

// ExpandInlineDerives resolves, typechecks, purity-checks, caches, and expands all
// non-Provider `type T derive E` clauses in a package using the unified pure
// ContentInput -> MappedErgo pipeline.
func ExpandInlineDerives(req check.DeriveExpandRequest) check.DeriveExpandResult {
	var pending []*pendingDerive
	nonDeriveNames := map[string]bool{}
	fileOfDecl := map[syntax.Decl]int{}
	declsByName := map[string][]syntax.Decl{}
	allOrigDecls := map[syntax.Decl]bool{}

	for fi, f := range req.Files {
		for _, d := range f.Decls {
			allOrigDecls[d] = true
			if td, ok := d.(*syntax.TypeDecl); ok && td.Derive != nil && td.TypeParams == nil && !syntax.IsProviderPlan(td.Derive) && td.Name != nil {
				ln, col := f.Position(td.Derive.Pos())
				u16Line, u16Col := OffsetLineColUTF16([]byte(f.Src), uint32(td.Derive.Pos()))
				pending = append(pending, &pendingDerive{
					index:   len(pending),
					fileIdx: fi,
					file:    f,
					decl:    td,
					target:  td.Name.Name,
					expr:    td.Derive,
					line:    u16Line,
					col:     u16Col,
					lineInt: ln,
					byteCol: col,
				})
				continue
			}
			fileOfDecl[d] = fi
			for _, name := range declDeclaredNames(d) {
				nonDeriveNames[name] = true
				declsByName[name] = append(declsByName[name], d)
			}
		}
	}

	res := check.DeriveExpandResult{
		ReplacedDecls: map[*syntax.TypeDecl]bool{},
		FileOrigins:   map[*syntax.File]int{},
		Record: &check.DeriveRecord{
			RefusedNames: map[string]bool{},
			RefusedRoots: map[string]bool{},
		},
	}
	if len(pending) == 0 {
		return res
	}

	genContexts := map[*syntax.File]*genFileContext{}
	seenTarget := map[string]*pendingDerive{}
	var activePending []*pendingDerive

	for _, pd := range pending {
		if nonDeriveNames[pd.target] {
			res.Diags = append(res.Diags, diagAtNode(pd.fileIdx, pd.file, pd.decl.Name, check.CodeType,
				fmt.Sprintf("%s is already declared in this package", pd.target)))
			pd.refused = true
			res.Record.RefusedRoots[pd.target] = true
			res.Record.RefusedNames[pd.target] = true
			continue
		}
		if prev := seenTarget[pd.target]; prev != nil {
			res.Diags = append(res.Diags, diagAtNode(pd.fileIdx, pd.file, pd.decl.Name, check.CodeType,
				fmt.Sprintf("%s is already declared in this package", pd.target)))
			pd.refused = true
			res.Record.RefusedRoots[pd.target] = true
			res.Record.RefusedNames[pd.target] = true
			continue
		}
		seenTarget[pd.target] = pd
		activePending = append(activePending, pd)
	}

	for len(activePending) > 0 {
		pendingTargets := map[string]*pendingDerive{}
		for _, pd := range activePending {
			pendingTargets[pd.target] = pd
		}
		progress := false
		var nextPending []*pendingDerive

		for _, pd := range activePending {
			if pd.done || pd.refused {
				continue
			}
			reach, idents := reachableClosure(pd.expr, declsByName)
			pd.reachIdents = idents

			if blockedBySelfOrPending(pd, idents, pendingTargets) {
				nextPending = append(nextPending, pd)
				continue
			}

			progress = true
			ok := expandOneDerive(req, pd, reach, idents, fileOfDecl, declsByName, genContexts, &res)
			if !ok {
				pd.refused = true
				res.Record.RefusedRoots[pd.target] = true
				res.Record.RefusedNames[pd.target] = true
			} else {
				pd.done = true
			}
		}

		if !progress && len(nextPending) > 0 {
			reportDeriveCycle(nextPending, declsByName, &res)
			break
		}
		activePending = nextPending
	}

	res.RemapDiag = func(f *syntax.File, d check.Diagnostic) (int, check.Diagnostic, bool) {
		ctx := genContexts[f]
		if ctx == nil {
			return 0, d, false
		}
		mapped := remapGeneratedDiag(ctx, d)
		return ctx.origFileIdx, mapped, true
	}

	return res
}

func blockedBySelfOrPending(pd *pendingDerive, idents map[string]bool, pendingTargets map[string]*pendingDerive) bool {
	for id := range idents {
		if target := matchingPendingTarget(id, pendingTargets); target != nil {
			if target == pd {
				return true
			}
			if !target.done && !target.refused {
				return true
			}
		}
	}
	return false
}

func matchingPendingTarget(ident string, pendingTargets map[string]*pendingDerive) *pendingDerive {
	if p := pendingTargets[ident]; p != nil {
		return p
	}
	for t, p := range pendingTargets {
		if len(ident) > len(t) && strings.HasSuffix(ident, t) {
			return p
		}
	}
	return nil
}

func reportDeriveCycle(stuck []*pendingDerive, declsByName map[string][]syntax.Decl, res *check.DeriveExpandResult) {
	byTarget := map[string]*pendingDerive{}
	for _, pd := range stuck {
		byTarget[pd.target] = pd
	}
	edges := map[string]string{}
	for _, pd := range stuck {
		_, idents := reachableClosure(pd.expr, declsByName)
		for id := range idents {
			if tgt := matchingPendingTarget(id, byTarget); tgt != nil {
				edges[pd.target] = tgt.target
				break
			}
		}
	}
	visited := map[string]bool{}
	for _, pd := range stuck {
		if visited[pd.target] {
			continue
		}
		var path []string
		posInPath := map[string]int{}
		cur := pd.target
		for cur != "" && !visited[cur] {
			if idx, ok := posInPath[cur]; ok {
				cycle := append(append([]string(nil), path[idx:]...), cur)
				msg := "content mapper cycle: " + strings.Join(cycle, " -> ")
				origin := byTarget[cycle[0]]
				if origin == nil {
					origin = pd
				}
				res.Diags = append(res.Diags, diagAtNode(origin.fileIdx, origin.file, origin.expr, check.CodeType, msg))
				break
			}
			posInPath[cur] = len(path)
			path = append(path, cur)
			cur = edges[cur]
		}
		for _, node := range path {
			visited[node] = true
			if p := byTarget[node]; p != nil {
				p.refused = true
				res.Record.RefusedRoots[p.target] = true
				res.Record.RefusedNames[p.target] = true
			}
		}
		if len(edges) == 0 || !visited[pd.target] {
			res.Diags = append(res.Diags, diagAtNode(pd.fileIdx, pd.file, pd.expr, check.CodeType,
				fmt.Sprintf("content mapper cycle involving derive %s", pd.target)))
			pd.refused = true
			res.Record.RefusedRoots[pd.target] = true
			res.Record.RefusedNames[pd.target] = true
		}
	}
}

func expandOneDerive(
	req check.DeriveExpandRequest,
	pd *pendingDerive,
	reach map[syntax.Decl]bool,
	reachIdents map[string]bool,
	fileOfDecl map[syntax.Decl]int,
	declsByName map[string][]syntax.Decl,
	genContexts map[*syntax.File]*genFileContext,
	res *check.DeriveExpandResult,
) bool {
	phaseFiles, synDecl, synRHS := buildPhase1Files(req.Files, res.GeneratedFiles, pd, reach, reachIdents)
	phasePkg, phaseDiags := check.Check(req.PkgPath, phaseFiles, req.Importer)
	if len(phaseDiags) > 0 {
		for _, d := range phaseDiags {
			if d.File >= 0 && d.File < len(req.Files) {
				res.Diags = append(res.Diags, d)
			} else if d.File >= len(req.Files) && d.File < len(phaseFiles) {
				gf := phaseFiles[d.File]
				if ctx := genContexts[gf]; ctx != nil {
					mapped := remapGeneratedDiag(ctx, d)
					mapped.File = ctx.origFileIdx
					res.Diags = append(res.Diags, mapped)
					continue
				}
				res.Diags = append(res.Diags, diagAtNode(pd.fileIdx, pd.file, pd.expr, d.Code, d.Message))
			} else {
				res.Diags = append(res.Diags, diagAtNode(pd.fileIdx, pd.file, pd.expr, d.Code, d.Message))
			}
		}
		return false
	}

	evalRes, evalErr := eval.EvalDeriveVar(phasePkg, synDecl, synRHS, nil, eval.DefaultMapperBudget())
	if evalErr != nil {
		res.Diags = append(res.Diags, diagFromMapperEvalErr(pd, evalErr))
		return false
	}
	if len(evalRes.ByteOrigins) > 0 {
		phaseToOrig := make(map[*syntax.File]*syntax.File, len(req.Files))
		for fi := 0; fi < len(req.Files) && fi < len(phaseFiles); fi++ {
			phaseToOrig[phaseFiles[fi]] = req.Files[fi]
		}
		for i := range evalRes.ByteOrigins {
			if orig, ok := phaseToOrig[evalRes.ByteOrigins[i].File]; ok {
				evalRes.ByteOrigins[i].File = orig
			}
		}
	}

	var out *eval.MapperOutput
	var cacheKey string
	var cacheHit bool
	var mapperPkgPath, mapperFuncName, mediaKind string
	var companions []eval.MapperCompanion
	var steps, bytesUsed int64

	switch evalRes.Kind {
	case eval.DeriveDirectOutput:
		out = evalRes.Direct
		mediaKind = "ergo/direct"
		steps = evalRes.Steps
		bytesUsed = evalRes.BytesUsed
		if verr := VerifySpans(uint32(len(out.Source)), 0, nil, out.Spans); verr != nil {
			res.Diags = append(res.Diags, diagAtNode(pd.fileIdx, pd.file, pd.expr, check.CodeType,
				fmt.Sprintf("content mapper returned invalid SourceSpanMap: %s", verr)))
			return false
		}
	case eval.DerivePlan, eval.DeriveFunc:
		mediaKind = evalRes.MediaKind
		if mediaKind == "" {
			mediaKind = "ergo/inline"
		}
		companions = evalRes.Companions
		if evalRes.MapperPkg != nil {
			mapperPkgPath = evalRes.MapperPkg.Path
		}
		if evalRes.MapperFn != nil {
			mapperFuncName = evalRes.MapperFn.Name
		}
		input := eval.MapperInput{
			Target: pd.target,
			Origin: eval.MapperOrigin{
				Kind:    eval.OriginInline,
				Package: req.PkgPath,
				File:    pd.file.Name,
				Target:  pd.target,
				Line:    pd.line,
				Col:     pd.col,
			},
			MediaKind:  mediaKind,
			Bytes:      evalRes.Bytes,
			Companions: companions,
		}
		runOut, key, hit, rErr := runMapperCached(req.Cache, req.BuildID, evalRes.MapperPkg, evalRes.MapperFn, evalRes.Callable, input, eval.DefaultMapperBudget())
		if rErr != nil {
			res.Diags = append(res.Diags, diagFromMapperEvalErr(pd, rErr))
			return false
		}
		out = runOut
		cacheKey = key
		cacheHit = hit
		steps = evalRes.Steps + out.Steps
		bytesUsed = evalRes.BytesUsed + out.BytesUsed
	default:
		res.Diags = append(res.Diags, diagAtNode(pd.fileIdx, pd.file, pd.expr, check.CodeType,
			fmt.Sprintf("unsupported derive evaluation result for %s", pd.target)))
		return false
	}

	rec := check.MapperRecord{
		Target:     pd.target,
		Kind:       "inline",
		FileName:   pd.file.Name,
		Line:       pd.line,
		Col:        pd.col,
		MapperPkg:  mapperPkgPath,
		MapperFunc: mapperFuncName,
		MediaKind:  mediaKind,
		CacheKey:   cacheKey,
		CacheHit:   cacheHit,
		Steps:      steps,
		BytesUsed:  bytesUsed,
		Generated:  out.Source,
		Spans:      toSpanEntries(out.Spans, companions),
	}

	hasErrors := false
	for _, md := range out.Diags {
		d, drec := mapperDiagToCheckDiag(pd, md, evalRes.ByteOrigins, companions)
		rec.Diags = append(rec.Diags, drec)
		if !md.Warning {
			hasErrors = true
			res.Diags = append(res.Diags, d)
		}
	}
	if hasErrors {
		res.Record.Records = append(res.Record.Records, rec)
		return false
	}

	genFile, parseErr := syntax.Parse(out.Source)
	if parseErr != nil {
		msg := fmt.Sprintf("generated Ergo for derive %s failed to parse: %v", pd.target, parseErr)
		if se, ok := parseErr.(*syntax.Error); ok {
			d := check.Diagnostic{
				Code:    se.Code,
				Message: se.Message,
				Offset:  se.Offset,
				Length:  se.Length,
				Line:    se.Line,
				Column:  se.Column,
			}
			ctx := &genFileContext{
				origFileIdx: pd.fileIdx,
				origFile:    pd.file,
				deriveExpr:  pd.expr,
				target:      pd.target,
				spans:       out.Spans,
				byteOrigins: evalRes.ByteOrigins,
				companions:  companions,
			}
			mapped := remapGeneratedDiag(ctx, d)
			mapped.Message = fmt.Sprintf("generated code for %s: %s", pd.target, se.Message)
			res.Diags = append(res.Diags, mapped)
		} else {
			res.Diags = append(res.Diags, diagAtNode(pd.fileIdx, pd.file, pd.expr, check.CodeSyntax, msg))
		}
		res.Record.Records = append(res.Record.Records, rec)
		return false
	}

	genFile.Name = fmt.Sprintf("__ergo_derive_%03d_%s.ergo", pd.index, pd.target)

	var genDecls []string
	declaredTargetType := false
	for _, gd := range genFile.Decls {
		for _, name := range declDeclaredNames(gd) {
			genDecls = append(genDecls, name)
			if td, ok := gd.(*syntax.TypeDecl); ok && td.Name != nil && td.Name.Name == pd.target {
				declaredTargetType = true
			}
			if _, collision := declsByName[name]; collision {
				res.Diags = append(res.Diags, diagAtNode(pd.fileIdx, pd.file, pd.expr, check.CodeType,
					fmt.Sprintf("%s is already declared in this package: the derive of %s generates it", name, pd.target)))
				res.Record.Records = append(res.Record.Records, rec)
				return false
			}
		}
	}
	if !declaredTargetType {
		res.Diags = append(res.Diags, diagAtNode(pd.fileIdx, pd.file, pd.expr, check.CodeType,
			fmt.Sprintf("content mapper output for derive %s did not declare type %s", pd.target, pd.target)))
		res.Record.Records = append(res.Record.Records, rec)
		return false
	}

	rec.Decls = genDecls
	res.Record.Records = append(res.Record.Records, rec)
	res.ReplacedDecls[pd.decl] = true
	res.GeneratedFiles = append(res.GeneratedFiles, genFile)
	res.FileOrigins[genFile] = pd.fileIdx
	genContexts[genFile] = &genFileContext{
		origFileIdx: pd.fileIdx,
		origFile:    pd.file,
		deriveExpr:  pd.expr,
		target:      pd.target,
		spans:       out.Spans,
		byteOrigins: evalRes.ByteOrigins,
		companions:  companions,
	}

	newFileIdx := len(req.Files) + len(res.GeneratedFiles) - 1
	for _, gd := range genFile.Decls {
		fileOfDecl[gd] = newFileIdx
		for _, name := range declDeclaredNames(gd) {
			declsByName[name] = append(declsByName[name], gd)
		}
	}
	return true
}

func buildPhase1Files(
	origFiles []*syntax.File,
	prevGenerated []*syntax.File,
	pd *pendingDerive,
	reach map[syntax.Decl]bool,
	reachIdents map[string]bool,
) ([]*syntax.File, *syntax.VarDecl, syntax.Expr) {
	synSpec := &syntax.VarSpec{
		Names:  []*syntax.Ident{{NamePos: pd.decl.Name.Pos(), Name: "__ergo_derive_val"}},
		Assign: pd.expr.Pos(),
		Values: []syntax.Expr{pd.expr},
	}
	synDecl := &syntax.VarDecl{
		Var:   pd.decl.Type,
		Mut:   syntax.NoPos,
		Specs: []*syntax.VarSpec{synSpec},
	}

	out := make([]*syntax.File, 0, len(origFiles)+len(prevGenerated))
	for fi, f := range origFiles {
		var kept []syntax.Decl
		usedInFile := map[string]bool{}
		if fi == pd.fileIdx {
			for k := range reachIdents {
				usedInFile[k] = true
			}
		}
		for _, d := range f.Decls {
			if reach[d] {
				for _, id := range collectNodeIdents(d) {
					usedInFile[id] = true
				}
			}
		}
		for _, d := range f.Decls {
			if idDecl, ok := d.(*syntax.ImportDecl); ok {
				if importUsedInPhase1(idDecl, usedInFile) {
					kept = append(kept, d)
				}
				continue
			}
			if reach[d] {
				kept = append(kept, d)
			}
		}
		if fi == pd.fileIdx {
			kept = append(kept, synDecl)
		}
		cp := *f
		cp.Decls = kept
		out = append(out, &cp)
	}
	out = append(out, prevGenerated...)
	return out, synDecl, pd.expr
}

func importUsedInPhase1(d *syntax.ImportDecl, used map[string]bool) bool {
	for _, sp := range d.Specs {
		var localName, pathVal string
		if sp.Path != nil {
			pathVal = sp.Path.Value
		}
		if sp.Name != nil {
			localName = sp.Name.Name
		} else if pathVal != "" {
			if syntax.IsRelativeAssetPath(pathVal) {
				localName = syntax.AssetStem(pathVal)
			} else if sp.Kind == syntax.ImportGo {
				localName = check.GoPackageName(pathVal)
			} else if idx := strings.LastIndexByte(pathVal, '/'); idx >= 0 {
				localName = pathVal[idx+1:]
			} else {
				localName = pathVal
			}
		}
		if localName == "_" || localName == "." || used[localName] || (pathVal != "" && used[syntax.AssetStem(pathVal)]) {
			return true
		}
	}
	return false
}

func reachableClosure(root syntax.Node, declsByName map[string][]syntax.Decl) (map[syntax.Decl]bool, map[string]bool) {
	reachDecls := map[syntax.Decl]bool{}
	reachIdents := map[string]bool{}
	var queue []string

	addIdent := func(name string) {
		if name == "" || reachIdents[name] {
			return
		}
		reachIdents[name] = true
		queue = append(queue, name)
	}
	for _, name := range collectNodeIdents(root) {
		addIdent(name)
	}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		for _, d := range declsByName[name] {
			if reachDecls[d] {
				continue
			}
			reachDecls[d] = true
			for _, sub := range collectNodeIdents(d) {
				addIdent(sub)
			}
		}
	}
	return reachDecls, reachIdents
}

func collectNodeIdents(root syntax.Node) []string {
	var out []string
	seen := map[string]bool{}
	syntax.Inspect(root, func(n syntax.Node) bool {
		if id, ok := n.(*syntax.Ident); ok && id.Name != "" && !seen[id.Name] {
			seen[id.Name] = true
			out = append(out, id.Name)
		}
		return true
	})
	return out
}

func declDeclaredNames(d syntax.Decl) []string {
	switch x := d.(type) {
	case *syntax.TypeDecl:
		var out []string
		if x.Name != nil {
			out = append(out, x.Name.Name)
		}
		if it, ok := x.Spec.(*syntax.InterfaceType); ok {
			for _, v := range it.Union {
				if vs, ok := v.(*syntax.VariantSpec); ok && vs.Name != nil {
					out = append(out, vs.Name.Name)
				}
			}
		}
		return out
	case *syntax.FuncDecl:
		if x.Recv == nil && x.Name != nil {
			return []string{x.Name.Name}
		}
	case *syntax.VarDecl:
		var out []string
		for _, sp := range x.Specs {
			for _, id := range sp.Names {
				out = append(out, id.Name)
			}
		}
		return out
	case *syntax.LawDecl:
		if x.Name != nil {
			return []string{x.Name.Name}
		}
	}
	return nil
}

func diagAtNode(fileIdx int, f *syntax.File, n syntax.Node, code, msg string) check.Diagnostic {
	pos := n.Pos()
	end := n.End()
	length := int(end - pos)
	if length < 1 {
		length = 1
	}
	line, col := f.Position(pos)
	return check.Diagnostic{
		Code:    code,
		Message: msg,
		File:    fileIdx,
		Offset:  int(pos),
		Length:  length,
		Line:    line,
		Column:  col,
	}
}

func diagFromMapperEvalErr(pd *pendingDerive, err *eval.MapperEvalError) check.Diagnostic {
	msg := err.Message
	if err.Frame != "" && !strings.Contains(msg, err.Frame) {
		msg = fmt.Sprintf("%s (in %s)", msg, err.Frame)
	}
	var at syntax.Node = pd.expr
	if err.At != nil && err.At.Pos() >= pd.file.Pos() && err.At.End() <= pd.file.End() {
		if inSameFile(pd.file, err.At) {
			at = err.At
		}
	}
	return diagAtNode(pd.fileIdx, pd.file, at, check.CodeType, msg)
}

func inSameFile(f *syntax.File, target syntax.Node) bool {
	found := false
	syntax.Inspect(f, func(n syntax.Node) bool {
		if n == target {
			found = true
			return false
		}
		return !found
	})
	return found
}

func mapperDiagToCheckDiag(
	pd *pendingDerive,
	md eval.MapperDiag,
	origins []eval.SourceByteOrigin,
	companions []eval.MapperCompanion,
) (check.Diagnostic, check.MapperDiagRecord) {
	msg := formatMapperDiagMessage(md)
	var diag check.Diagnostic
	var rec check.MapperDiagRecord
	rec.Warning = md.Warning
	rec.HasCompanion = md.HasCompanion
	rec.Companion = md.Companion
	rec.SrcStart = md.Span.Start
	rec.SrcEnd = md.Span.End
	rec.Code = md.Code
	rec.Message = md.Message
	rec.Hint = md.Hint

	if md.HasCompanion && int(md.Companion) < len(companions) {
		comp := companions[md.Companion]
		rec.CompanionPath = comp.Path
		cl, cc := OffsetLineColByte(comp.Bytes, md.Span.Start)
		rec.MappedFile = comp.Path
		rec.MappedLine = uint32(cl)
		rec.MappedCol = uint32(cc)
		diag = diagAtNode(pd.fileIdx, pd.file, pd.expr, check.CodeType,
			fmt.Sprintf("%s (in companion %s:%d:%d)", msg, comp.Path, cl, cc))
		return diag, rec
	}

	if d, ok := resolveInlineSpan(pd.fileIdx, pd.file, origins, md.Span, check.CodeType, msg); ok {
		rec.MappedFile = pd.file.Name
		rec.MappedLine = uint32(d.Line)
		rec.MappedCol = uint32(d.Column)
		return d, rec
	}

	diag = diagAtNode(pd.fileIdx, pd.file, pd.expr, check.CodeType, msg)
	rec.MappedFile = pd.file.Name
	rec.MappedLine = uint32(diag.Line)
	rec.MappedCol = uint32(diag.Column)
	return diag, rec
}

func formatMapperDiagMessage(md eval.MapperDiag) string {
	msg := md.Message
	if md.Code != "" {
		msg = md.Code + ": " + msg
	}
	if md.Hint != "" {
		msg += " (" + md.Hint + ")"
	}
	return msg
}

func resolveInlineSpan(
	fileIdx int,
	origFile *syntax.File,
	origins []eval.SourceByteOrigin,
	sp eval.MapperSpan,
	code string,
	msg string,
) (check.Diagnostic, bool) {
	if len(origins) == 0 || sp.Start > uint32(len(origins)) || sp.End > uint32(len(origins)) {
		return check.Diagnostic{}, false
	}
	if sp.End > sp.Start {
		first := origins[sp.Start]
		last := origins[sp.End-1]
		if first.File == origFile && last.File == origFile && first.Start.IsValid() && last.End.IsValid() && last.End >= first.Start {
			line, col := origFile.Position(first.Start)
			length := int(last.End - first.Start)
			if length < 1 {
				length = 1
			}
			return check.Diagnostic{
				Code:    code,
				Message: msg,
				File:    fileIdx,
				Offset:  int(first.Start),
				Length:  length,
				Line:    line,
				Column:  col,
			}, true
		}
		return check.Diagnostic{}, false
	}
	var pos syntax.Pos
	if int(sp.Start) < len(origins) && origins[sp.Start].File == origFile {
		pos = origins[sp.Start].Start
	} else if len(origins) > 0 && int(sp.Start) == len(origins) && origins[len(origins)-1].File == origFile {
		pos = origins[len(origins)-1].End
	}
	if !pos.IsValid() {
		return check.Diagnostic{}, false
	}
	line, col := origFile.Position(pos)
	return check.Diagnostic{
		Code:    code,
		Message: msg,
		File:    fileIdx,
		Offset:  int(pos),
		Length:  1,
		Line:    line,
		Column:  col,
	}, true
}

func remapGeneratedDiag(ctx *genFileContext, d check.Diagnostic) check.Diagnostic {
	if entry, ok := LookupGeneratedSpan(ctx.spans, uint32(d.Offset), uint32(d.Length)); ok {
		if entry.HasCompanion && int(entry.Companion) < len(ctx.companions) {
			comp := ctx.companions[entry.Companion]
			cl, cc := OffsetLineColByte(comp.Bytes, entry.Source.Start)
			base := diagAtNode(ctx.origFileIdx, ctx.origFile, ctx.deriveExpr, d.Code,
				fmt.Sprintf("%s (derived from %s; companion %s:%d:%d)", d.Message, ctx.target, comp.Path, cl, cc))
			base.Fix = d.Fix
			return base
		}
		if mapped, ok := resolveInlineSpan(ctx.origFileIdx, ctx.origFile, ctx.byteOrigins, entry.Source, d.Code, d.Message); ok {
			mapped.Fix = d.Fix
			return mapped
		}
	}
	base := diagAtNode(ctx.origFileIdx, ctx.origFile, ctx.deriveExpr, d.Code,
		fmt.Sprintf("%s (in derived declaration for %s)", d.Message, ctx.target))
	base.Fix = d.Fix
	return base
}

func toSpanEntries(spans []eval.MapperSpanMapping, companions []eval.MapperCompanion) []check.SpanEntry {
	out := make([]check.SpanEntry, len(spans))
	for i, sp := range spans {
		e := check.SpanEntry{
			GenStart:     sp.Generated.Start,
			GenEnd:       sp.Generated.End,
			HasCompanion: sp.HasCompanion,
			Companion:    sp.Companion,
			SrcStart:     sp.Source.Start,
			SrcEnd:       sp.Source.End,
			Label:        sp.Label,
		}
		if sp.HasCompanion && int(sp.Companion) < len(companions) {
			e.CompanionPath = companions[sp.Companion].Path
		}
		out[i] = e
	}
	return out
}
