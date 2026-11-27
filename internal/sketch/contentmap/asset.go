package contentmap

import (
	"fmt"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/syntax"
)

// FileAssetRequest describes a file-level content import (`import ./path.ext`)
// evaluated through the unified ContentInput -> MappedErgo contract.
type FileAssetRequest struct {
	Target       string
	AssetPath    string
	PackagePath  string
	MediaKind    string
	Bytes        []byte
	Companions   []eval.MapperCompanion
	MapperPkg    *check.Package
	MapperFn     *check.Func
	Cache        check.MapperCache
	BuildID      string
	ImportFile   string
	ImportOffset int
	ImportLength int
	ImportLine   int
	ImportCol    int
}

// AssetDiagnostic is a span-mapped diagnostic emitted from a file-asset mapper
// or from frontend compilation of its generated Ergo source.
type AssetDiagnostic struct {
	File    string
	Offset  int
	Length  int
	Line    int
	Column  int
	Code    string
	Message string
	Warning bool
}

// FileAssetResult holds the synthesized in-memory Ergo file, inspection record,
// and span-mapped diagnostics for one file-level content import.
type FileAssetResult struct {
	GenFile   *syntax.File
	Source    string
	Spans     []eval.MapperSpanMapping
	Record    check.MapperRecord
	Diags     []AssetDiagnostic
	Broken    bool
	RemapDiag func(check.Diagnostic) AssetDiagnostic
}

// RunFileAsset validates purity, executes or reuses lawcache for a file-level
// content mapper, verifies SourceSpanMap, checks MapperDiags before parsing,
// and parses MappedErgo.Source into an in-memory *syntax.File.
func RunFileAsset(req FileAssetRequest) FileAssetResult {
	res := FileAssetResult{
		Record: check.MapperRecord{
			Target:     req.Target,
			Kind:       "file",
			AssetPath:  req.AssetPath,
			FileName:   req.ImportFile,
			Line:       uint32(req.ImportLine),
			Col:        uint32(req.ImportCol),
			MapperPkg:  pkgPathOf(req.MapperPkg),
			MapperFunc: funcNameOf(req.MapperFn),
			MediaKind:  req.MediaKind,
		},
	}

	fallbackDiag := func(code, msg string) AssetDiagnostic {
		return AssetDiagnostic{
			File:    req.ImportFile,
			Offset:  req.ImportOffset,
			Length:  maxInt(req.ImportLength, 1),
			Line:    req.ImportLine,
			Column:  req.ImportCol,
			Code:    code,
			Message: msg,
		}
	}

	if req.MapperFn == nil {
		res.Broken = true
		res.Diags = []AssetDiagnostic{fallbackDiag(check.CodeType, fmt.Sprintf("missing mapper function for asset %s", req.AssetPath))}
		return res
	}
	if err := eval.CheckMapperFunc(req.MapperFn); err != nil {
		res.Broken = true
		res.Diags = []AssetDiagnostic{fallbackDiag(check.CodeType, err.Error())}
		return res
	}
	if err := eval.ValidateMapperPurity(req.MapperPkg, req.MapperFn); err != nil {
		res.Broken = true
		res.Diags = []AssetDiagnostic{fallbackDiag(check.CodeType, err.Message)}
		return res
	}

	input := eval.MapperInput{
		Target: req.Target,
		Origin: eval.MapperOrigin{
			Kind:    eval.OriginFile,
			Path:    req.AssetPath,
			Package: req.PackagePath,
		},
		MediaKind:  req.MediaKind,
		Bytes:      req.Bytes,
		Companions: req.Companions,
	}

	out, cacheKey, cacheHit, rErr := runMapperCached(req.Cache, req.BuildID, req.MapperPkg, req.MapperFn, req.MapperFn, input, eval.DefaultMapperBudget())
	if rErr != nil {
		res.Broken = true
		msg := rErr.Message
		if rErr.Frame != "" && !strings.Contains(msg, rErr.Frame) {
			msg = fmt.Sprintf("%s (in %s)", msg, rErr.Frame)
		}
		res.Diags = []AssetDiagnostic{fallbackDiag(check.CodeType, msg)}
		return res
	}

	res.Source = out.Source
	res.Spans = out.Spans
	res.Record.CacheKey = cacheKey
	res.Record.CacheHit = cacheHit
	res.Record.Steps = out.Steps
	res.Record.BytesUsed = out.BytesUsed
	res.Record.Generated = out.Source
	res.Record.Spans = toSpanEntries(out.Spans, req.Companions)

	importCtxSuffix := ""
	if req.ImportFile != "" && req.ImportLine > 0 {
		importCtxSuffix = fmt.Sprintf(" (imported from %s:%d:%d)", req.ImportFile, req.ImportLine, req.ImportCol)
	}

	resolveSourceBytes := func(hasComp bool, compIdx uint32, sp eval.MapperSpan, code, msg string) (AssetDiagnostic, check.MapperDiagRecord) {
		drec := check.MapperDiagRecord{
			HasCompanion: hasComp,
			Companion:    compIdx,
			SrcStart:     sp.Start,
			SrcEnd:       sp.End,
			Code:         code,
			Message:      msg,
		}
		targetFile := req.AssetPath
		targetBytes := req.Bytes
		if hasComp && int(compIdx) < len(req.Companions) {
			targetFile = req.Companions[compIdx].Path
			targetBytes = req.Companions[compIdx].Bytes
			drec.CompanionPath = targetFile
		}
		line, col := OffsetLineColByte(targetBytes, sp.Start)
		length := int(sp.End - sp.Start)
		if length < 1 {
			length = 1
		}
		drec.MappedFile = targetFile
		drec.MappedLine = uint32(line)
		drec.MappedCol = uint32(col)
		return AssetDiagnostic{
			File:    targetFile,
			Offset:  int(sp.Start),
			Length:  length,
			Line:    line,
			Column:  col,
			Code:    check.CodeType,
			Message: msg + importCtxSuffix,
		}, drec
	}

	hasErrors := false
	for _, md := range out.Diags {
		msg := formatMapperDiagMessage(md)
		d, drec := resolveSourceBytes(md.HasCompanion, md.Companion, md.Span, md.Code, msg)
		drec.Warning = md.Warning
		drec.Message = md.Message
		drec.Hint = md.Hint
		d.Warning = md.Warning
		res.Record.Diags = append(res.Record.Diags, drec)
		if !md.Warning {
			hasErrors = true
			res.Diags = append(res.Diags, d)
		}
	}
	if hasErrors {
		res.Broken = true
		return res
	}

	remap := func(d check.Diagnostic) AssetDiagnostic {
		if entry, ok := LookupGeneratedSpan(out.Spans, uint32(d.Offset), uint32(d.Length)); ok {
			mapped, _ := resolveSourceBytes(entry.HasCompanion, entry.Companion, entry.Source, d.Code, d.Message)
			mapped.Code = d.Code
			return mapped
		}
		return AssetDiagnostic{
			File:    req.AssetPath,
			Offset:  0,
			Length:  maxInt(len(req.Bytes), 1),
			Line:    1,
			Column:  1,
			Code:    d.Code,
			Message: fmt.Sprintf("%s (generated from %s)%s", d.Message, req.AssetPath, importCtxSuffix),
		}
	}
	res.RemapDiag = remap

	genFile, parseErr := syntax.Parse(out.Source)
	if parseErr != nil {
		res.Broken = true
		if se, ok := parseErr.(*syntax.Error); ok {
			d := check.Diagnostic{
				Code:    se.Code,
				Message: fmt.Sprintf("generated Ergo failed to parse: %s", se.Message),
				Offset:  se.Offset,
				Length:  se.Length,
				Line:    se.Line,
				Column:  se.Column,
			}
			res.Diags = append(res.Diags, remap(d))
		} else {
			res.Diags = append(res.Diags, fallbackDiag(check.CodeSyntax, fmt.Sprintf("generated Ergo for %s failed to parse: %v", req.AssetPath, parseErr)))
		}
		return res
	}

	genFile.Name = req.AssetPath + ".ergo"
	var declNames []string
	for _, gd := range genFile.Decls {
		declNames = append(declNames, declDeclaredNames(gd)...)
	}
	res.Record.Decls = declNames
	res.GenFile = genFile
	return res
}

func pkgPathOf(p *check.Package) string {
	if p == nil {
		return ""
	}
	return p.Path
}

func funcNameOf(f *check.Func) string {
	if f == nil {
		return ""
	}
	return f.Name
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
