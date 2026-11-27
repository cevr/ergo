package contentmap

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/lawcache"
	"ergo.local/prototype/internal/sketch/syntax"
)

type cachedPayload struct {
	Version int                      `json:"v"`
	Source  string                   `json:"source"`
	Spans   []eval.MapperSpanMapping `json:"spans,omitempty"`
	Diags   []eval.MapperDiag        `json:"diags,omitempty"`
	Digest  string                   `json:"digest,omitempty"`
}

const cachePayloadVersion = 1

// MapperSemanticDigest computes a deterministic SHA-256 digest of the canonical
// AST/token semantics reachable from fn (or mapperPkg when fn is nil).
// Comments, whitespace, line numbers, file paths, and unreachable declarations
// are excluded so formatting/comment edits and unrelated changes preserve cache hits.
func MapperSemanticDigest(pkg *check.Package, fn *check.Func) string {
	h := sha256.New()
	if pkg == nil {
		fmt.Fprintf(h, "nilpkg")
		return hex.EncodeToString(h.Sum(nil))
	}
	fmt.Fprintf(h, "pkg:%s|", pkg.Path)

	seenPkgs := map[*check.Package]bool{}
	seenFuncs := map[*check.Func]bool{}
	seenVars := map[*check.Var]bool{}
	seenTypes := map[*check.TypeName]bool{}
	var declQueue []struct {
		p *check.Package
		n syntax.Node
	}
	var initQueue []*check.Package

	enqueuePkg := func(p *check.Package) {
		if p == nil || p.Info == nil || seenPkgs[p] {
			return
		}
		seenPkgs[p] = true
		initQueue = append(initQueue, p)
	}

	var visitNode func(p *check.Package, n syntax.Node)
	visitNode = func(p *check.Package, n syntax.Node) {
		if n == nil || p == nil || p.Info == nil {
			return
		}
		enqueuePkg(p)
		declQueue = append(declQueue, struct {
			p *check.Package
			n syntax.Node
		}{p: p, n: n})
		syntax.Inspect(n, func(sub syntax.Node) bool {
			if sel, ok := sub.(*syntax.SelectorExpr); ok {
				if id, ok := sel.X.(*syntax.Ident); ok {
					if pn, ok := p.Info.Uses[id].(*check.PkgName); ok && pn.Pkg != nil {
						visitNamedTarget(pn.Pkg, sel.Sel.Name, seenFuncs, seenVars, seenTypes, visitNode)
					}
				}
			}
			id, ok := sub.(*syntax.Ident)
			if !ok {
				return true
			}
			obj := p.Info.Uses[id]
			if obj == nil {
				return true
			}
			switch o := obj.(type) {
			case *check.Func:
				if !seenFuncs[o] {
					seenFuncs[o] = true
					targetPkg := o.Pkg
					if targetPkg == nil {
						targetPkg = p
					}
					if o.Decl != nil {
						visitNode(targetPkg, o.Decl)
					} else {
						fmt.Fprintf(h, "fn:%s.%s|", targetPkg.Path, o.Name)
					}
				}
			case *check.Var:
				if !seenVars[o] {
					seenVars[o] = true
					if vd := findVarDecl(p, o); vd != nil {
						visitNode(p, vd)
					}
				}
			case *check.TypeName:
				if !seenTypes[o] && o.Pkg != nil {
					seenTypes[o] = true
					if o.Decl != nil {
						visitNode(o.Pkg, o.Decl)
					}
				}
			}
			return true
		})
	}

	enqueuePkg(pkg)
	if fn != nil {
		seenFuncs[fn] = true
		if fn.Decl != nil {
			visitNode(pkg, fn.Decl)
		} else {
			fmt.Fprintf(h, "rootfn:%s|", fn.Name)
		}
	} else {
		for _, f := range pkg.Funcs {
			if f != nil && f.Decl != nil && !seenFuncs[f] {
				seenFuncs[f] = true
				visitNode(pkg, f.Decl)
			}
		}
		for _, tn := range pkg.Types {
			if tn != nil && tn.Decl != nil && !seenTypes[tn] {
				seenTypes[tn] = true
				visitNode(pkg, tn.Decl)
			}
		}
	}

	for len(initQueue) > 0 {
		p := initQueue[0]
		initQueue = initQueue[1:]
		fmt.Fprintf(h, "initpkg:%s|", p.Path)
		for _, ini := range p.Info.InitOrder {
			visitNode(p, ini.Value)
		}
	}

	for _, item := range declQueue {
		fmt.Fprintf(h, "decl:%s|", item.p.Path)
		writeCanonicalNode(h, item.n)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func visitNamedTarget(
	targetPkg *check.Package,
	name string,
	seenFuncs map[*check.Func]bool,
	seenVars map[*check.Var]bool,
	seenTypes map[*check.TypeName]bool,
	visitNode func(*check.Package, syntax.Node),
) {
	if targetPkg == nil || targetPkg.Info == nil {
		return
	}
	for _, f := range targetPkg.Funcs {
		if f != nil && f.Name == name && !seenFuncs[f] {
			seenFuncs[f] = true
			if f.Decl != nil {
				visitNode(targetPkg, f.Decl)
			}
			return
		}
	}
	for _, tn := range targetPkg.Types {
		if tn != nil && tn.Name == name && !seenTypes[tn] {
			seenTypes[tn] = true
			if tn.Decl != nil {
				visitNode(targetPkg, tn.Decl)
			}
			return
		}
	}
	for _, f := range targetPkg.Files() {
		for _, d := range f.Decls {
			vd, ok := d.(*syntax.VarDecl)
			if !ok {
				continue
			}
			for _, sp := range vd.Specs {
				for _, id := range sp.Names {
					if id.Name == name {
						if v, ok := targetPkg.Info.Defs[id].(*check.Var); ok && !seenVars[v] {
							seenVars[v] = true
							visitNode(targetPkg, vd)
						}
						return
					}
				}
			}
		}
	}
}

func findVarDecl(p *check.Package, v *check.Var) syntax.Node {
	if p == nil || p.Info == nil || v == nil {
		return nil
	}
	for _, f := range p.Files() {
		for _, d := range f.Decls {
			vd, ok := d.(*syntax.VarDecl)
			if !ok {
				continue
			}
			for _, sp := range vd.Specs {
				for _, id := range sp.Names {
					if p.Info.Defs[id] == v {
						return vd
					}
				}
			}
		}
	}
	return nil
}

func writeCanonicalNode(h hash.Hash, root syntax.Node) {
	if root == nil {
		return
	}
	syntax.Inspect(root, func(n syntax.Node) bool {
		if n == nil {
			return false
		}
		switch n.(type) {
		case *syntax.Comment, *syntax.CommentGroup:
			return false
		}
		fmt.Fprintf(h, "(%T", n)
		switch x := n.(type) {
		case *syntax.Ident:
			fmt.Fprintf(h, ":%s", x.Name)
		case *syntax.BasicLit:
			fmt.Fprintf(h, ":%d:%s", x.Kind, x.Value)
		case *syntax.BinaryExpr:
			fmt.Fprintf(h, ":%d", x.Op)
		case *syntax.UnaryExpr:
			fmt.Fprintf(h, ":%d", x.Op)
		case *syntax.AssignStmt:
			fmt.Fprintf(h, ":%d", x.Tok)
		case *syntax.BranchStmt:
			fmt.Fprintf(h, ":%d", x.Tok)
		case *syntax.ImportSpec:
			var name string
			if x.Name != nil {
				name = x.Name.Name
			}
			var path string
			if x.Path != nil {
				path = x.Path.Value
			}
			fmt.Fprintf(h, ":%d:%s:%s", x.Kind, name, path)
		case *syntax.TypeDecl:
			if x.Where != nil {
				fmt.Fprintf(h, ":where")
			}
			if x.Derive != nil {
				fmt.Fprintf(h, ":derive")
			}
		case *syntax.FuncDecl:
			if x.Extern != nil {
				fmt.Fprintf(h, ":extern")
			}
		case *syntax.FuncType:
			fmt.Fprintf(h, ":entry=%v:defects=%v", x.Entry, x.Defects)
		case *syntax.Field:
			fmt.Fprintf(h, ":inc=%v", x.Include)
		case *syntax.CompositeLit:
			fmt.Fprintf(h, ":dotdot=%v", x.Base != nil)
		case *syntax.CallExpr:
			fmt.Fprintf(h, ":trail=%v:tf=%v", x.Trailing != nil, x.TrailingFunc)
		case *syntax.StarExpr:
			fmt.Fprintf(h, ":mut=%v", x.Mut)
		}
		h.Write([]byte(")"))
		return true
	})
}

// BuildCacheKey builds a sound content-addressed key for one mapper invocation.
// Companion order is preserved verbatim (as index 0..N-1 affects SpanMapping.Companion).
// Inline Line, Col, and File are excluded so unrelated edits, comments, and line
// shifts rebase diagnostics and hit cache without re-evaluating the mapper.
func BuildCacheKey(buildID, mapperDigest string, input eval.MapperInput) string {
	bSum := sha256.Sum256(input.Bytes)
	originTag := "inline"
	originPath := ""
	if input.Origin.Kind == eval.OriginFile {
		originTag = "file"
		originPath = input.Origin.Path
	}
	parts := []string{
		"contentmap-v1",
		buildID,
		mapperDigest,
		originTag,
		input.Origin.Package,
		originPath,
		input.Target,
		input.MediaKind,
		hex.EncodeToString(bSum[:]),
	}
	for i, c := range input.Companions {
		cSum := sha256.Sum256(c.Bytes)
		parts = append(parts, fmt.Sprintf("comp[%d]:%s=%s", i, c.Path, hex.EncodeToString(cSum[:])))
	}
	return lawcache.Key(parts...)
}

// GetCached returns a previously stored validated MapperOutput for key.
func GetCached(store check.MapperCache, key string) (*eval.MapperOutput, bool) {
	if store == nil || key == "" {
		return nil, false
	}
	raw, ok := store.Get(key)
	if !ok || raw == "" {
		return nil, false
	}
	var p cachedPayload
	if err := json.Unmarshal([]byte(raw), &p); err != nil || p.Version != cachePayloadVersion {
		return nil, false
	}
	var digest []byte
	if p.Digest != "" {
		d, err := hex.DecodeString(p.Digest)
		if err != nil {
			return nil, false
		}
		digest = d
	}
	return &eval.MapperOutput{
		Source: p.Source,
		Spans:  p.Spans,
		Diags:  p.Diags,
		Digest: digest,
	}, true
}

// PutCached stores a validated MapperOutput in store under key.
func PutCached(store check.MapperCache, key string, out *eval.MapperOutput) {
	if store == nil || key == "" || out == nil {
		return
	}
	p := cachedPayload{
		Version: cachePayloadVersion,
		Source:  out.Source,
		Spans:   out.Spans,
		Diags:   out.Diags,
		Digest:  hex.EncodeToString(out.Digest),
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return
	}
	store.Keep(key, string(raw))
}

func companionLens(comps []eval.MapperCompanion) []uint32 {
	out := make([]uint32, len(comps))
	for i, c := range comps {
		out[i] = uint32(len(c.Bytes))
	}
	return out
}

// runMapperCached executes callable on input with optional cache reuse and strict
// span validation. Budget/purity/ffi/defect/bad-span errors are never cached.
func runMapperCached(
	store check.MapperCache,
	buildID string,
	mapperPkg *check.Package,
	mapperFn *check.Func,
	callable any,
	input eval.MapperInput,
	budget eval.MapperBudget,
) (out *eval.MapperOutput, cacheKey string, cacheHit bool, err *eval.MapperEvalError) {
	digest := MapperSemanticDigest(mapperPkg, mapperFn)
	cacheKey = BuildCacheKey(buildID, digest, input)
	if cached, ok := GetCached(store, cacheKey); ok {
		if verr := VerifySpans(uint32(len(cached.Source)), uint32(len(input.Bytes)), companionLens(input.Companions), cached.Spans); verr == nil {
			return cached, cacheKey, true, nil
		}
	}
	resOut, evalErr := eval.InvokeMapper(mapperPkg, callable, input, budget)
	if evalErr != nil {
		return nil, cacheKey, false, evalErr
	}
	if verr := VerifySpans(uint32(len(resOut.Source)), uint32(len(input.Bytes)), companionLens(input.Companions), resOut.Spans); verr != nil {
		return nil, cacheKey, false, &eval.MapperEvalError{
			Kind:    "contract",
			Message: fmt.Sprintf("content mapper returned invalid SourceSpanMap: %s", verr),
		}
	}
	PutCached(store, cacheKey, resOut)
	return resOut, cacheKey, false, nil
}
