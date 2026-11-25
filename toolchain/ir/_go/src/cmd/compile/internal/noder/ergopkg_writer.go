package noder

import (
	"fmt"
	"go/constant"
	"internal/buildcfg"
	"internal/pkgbits"
	"math"
	"strings"

	"cmd/compile/internal/base"
	"cmd/compile/internal/ergoir"
	"cmd/compile/internal/ir"
)

type ergoStubEncoder struct {
	pkgbits.PkgEncoder
	eir *ergoir.EIRPackage

	posBaseIdx []index // per HFiles index
	pkgIdxs    []index // 0 = local, 1.. = HImports index
	typIdxs    []index // per HTypes 0-based index (idx+1 stored so 0 means unset)
	objIdxs    []index // per HSymbols 0-based index (idx+1 stored so 0 means unset)
	bodyOfSym  map[uint32]int

	err error
}

func writeErgoPkgStub(eir *ergoir.EIRPackage) (string, error) {
	enc := &ergoStubEncoder{
		PkgEncoder: pkgbits.NewPkgEncoder(uirVersion, base.Debug.SyncFrames),
		eir:        eir,
		posBaseIdx: make([]index, len(eir.Files)),
		pkgIdxs:    make([]index, len(eir.Imports)+1),
		typIdxs:    make([]index, len(eir.Types)),
		objIdxs:    make([]index, len(eir.Symbols)),
		bodyOfSym:  make(map[uint32]int, len(eir.Bodies)),
	}
	for i, b := range eir.Bodies {
		enc.bodyOfSym[b.FuncSym] = i
	}
	for i := range enc.posBaseIdx {
		enc.posBaseIdx[i] = enc.encodePosBase(eir.Files[i].Path)
	}
	for i := 0; i < len(eir.Imports); i++ {
		enc.pkgIdxs[i+1] = enc.encodePkg(eir.Imports[i].Path, eir.Imports[i].Name, nil)
	}
	impRefs := make([]index, len(eir.Imports))
	for i := range eir.Imports {
		impRefs[i] = enc.pkgIdxs[i+1]
	}
	enc.pkgIdxs[0] = enc.encodePkg("", eir.PkgName, impRefs)

	// PublicRootIdx (0) and PrivateRootIdx (1) must be allocated first in SectionMeta.
	pub := enc.NewEncoder(pkgbits.SectionMeta, pkgbits.SyncPublic)
	priv := enc.NewEncoder(pkgbits.SectionMeta, pkgbits.SyncPrivate)
	assert(pub.Idx == pkgbits.PublicRootIdx)
	assert(priv.Idx == pkgbits.PrivateRootIdx)

	// Collect package-scope local declarations.
	var pubSyms []uint32
	var typeSyms, varSyms, funcSyms []uint32
	for i, sym := range eir.Symbols {
		if sym.Import != 0 {
			continue
		}
		id := uint32(i + 1)
		switch sym.Kind {
		case ergoir.HSType:
			pubSyms = append(pubSyms, id)
			typeSyms = append(typeSyms, id)
		case ergoir.HSVar:
			pubSyms = append(pubSyms, id)
			varSyms = append(varSyms, id)
		case ergoir.HSFunc:
			if strings.HasPrefix(sym.Name, "init.") {
				continue
			}
			pubSyms = append(pubSyms, id)
			funcSyms = append(funcSyms, id)
		}
	}

	// Write PublicRoot.
	{
		enc.writePkgRef(pub, enc.pkgIdxs[0])
		if pub.Version().Has(pkgbits.HasInit) {
			pub.Bool(false)
		}
		pub.Len(len(pubSyms))
		for _, sid := range pubSyms {
			enc.writeObjRef(pub, enc.encodeObj(sid), nil)
		}
		pub.Sync(pkgbits.SyncEOF)
		pub.Flush()
	}

	// Write PrivateRoot (pkgInit).
	{
		priv.Len(0) // cgoPragmas
		priv.Len(0) // pkgInitOrder
		priv.Sync(pkgbits.SyncDecls)

		for _, sid := range typeSyms {
			priv.Code(declOther)
			enc.writePkgObjs(priv, sid)
		}
		for _, sid := range varSyms {
			priv.Code(declVar)
			enc.writePkgObjs(priv, sid)
			priv.Len(0) // embeds
		}
		for _, sid := range funcSyms {
			priv.Code(declFunc)
			enc.writePkgObjs(priv, sid)
		}
		// Emit package initializers in EXACT HInit semantic order.
		for _, sid := range eir.Inits {
			priv.Code(declFunc)
			enc.writePkgObjs(priv, sid)
		}

		priv.Code(declEnd)
		priv.Sync(pkgbits.SyncEOF)
		priv.Flush()
	}

	if enc.err != nil {
		return "", enc.err
	}

	var sb strings.Builder
	enc.DumpTo(&sb)
	return sb.String(), nil
}

func (e *ergoStubEncoder) failf(format string, args ...any) {
	if e.err == nil {
		e.err = fmt.Errorf("ergopkg: "+format, args...)
	}
}

func (e *ergoStubEncoder) encodePosBase(filename string) index {
	w := e.NewEncoder(pkgbits.SectionPosBase, pkgbits.SyncPosBase)
	w.String(filename)
	w.Bool(true) // file base
	return w.Flush()
}

func (e *ergoStubEncoder) writePos(w *pkgbits.Encoder, spanID uint32) {
	w.Sync(pkgbits.SyncPos)
	if spanID == 0 {
		w.Bool(false)
		return
	}
	w.Bool(true)
	fileIdx, line, col := e.eir.SpanPos(spanID)
	w.Reloc(pkgbits.SectionPosBase, e.posBaseIdx[fileIdx])
	w.Uint(line)
	w.Uint(col)
}

func (e *ergoStubEncoder) encodePkg(path, name string, imports []index) index {
	w := e.NewEncoder(pkgbits.SectionPkg, pkgbits.SyncPkgDef)
	w.String(path)
	w.String(name)
	w.Len(len(imports))
	for _, imp := range imports {
		e.writePkgRef(w, imp)
	}
	return w.Flush()
}

func (e *ergoStubEncoder) writePkgRef(w *pkgbits.Encoder, idx index) {
	w.Sync(pkgbits.SyncPkg)
	w.Reloc(pkgbits.SectionPkg, idx)
}

func (e *ergoStubEncoder) writeQualifiedIdent(w *pkgbits.Encoder, pkgIdx index, name string) {
	w.Sync(pkgbits.SyncSym)
	e.writePkgRef(w, pkgIdx)
	w.String(name)
}

func (e *ergoStubEncoder) writeLocalIdent(w *pkgbits.Encoder, pkgIdx index, name string) {
	w.Sync(pkgbits.SyncLocalIdent)
	e.writePkgRef(w, pkgIdx)
	w.String(name)
}

func (e *ergoStubEncoder) writeSelector(w *pkgbits.Encoder, pkgIdx index, name string) {
	w.Sync(pkgbits.SyncSelector)
	e.writePkgRef(w, pkgIdx)
	w.String(name)
}

func memberShortName(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 && i+1 < len(name) {
		return name[i+1:]
	}
	return name
}

func (e *ergoStubEncoder) writeTypRef(w *pkgbits.Encoder, typeID uint32) {
	w.Sync(pkgbits.SyncType)
	w.Bool(false) // not derived
	w.Reloc(pkgbits.SectionType, e.encodeType(typeID))
}

func (e *ergoStubEncoder) encodeType(typeID uint32) index {
	if existing := e.typIdxs[typeID-1]; existing != 0 {
		return existing - 1
	}

	t := e.eir.Types[typeID-1]
	w := e.NewEncoder(pkgbits.SectionType, pkgbits.SyncTypeIdx)
	e.typIdxs[typeID-1] = w.Idx + 1

	switch t.Kind {
	case ergoir.HTBasic:
		if t.Aux == ergoir.HBasicAny {
			w.Code(pkgbits.TypeInterface)
			w.Len(0) // methods
			w.Len(0) // embeddeds
		} else {
			w.Code(pkgbits.TypeBasic)
			w.Len(int(t.Aux)) // 1..17 match types2.Bool..types2.String 1:1
		}
	case ergoir.HTPointer:
		w.Code(pkgbits.TypePointer)
		e.writeTypRef(w, e.eir.Operands[t.FirstOp])
	case ergoir.HTSlice:
		w.Code(pkgbits.TypeSlice)
		e.writeTypRef(w, e.eir.Operands[t.FirstOp])
	case ergoir.HTArray:
		w.Code(pkgbits.TypeArray)
		w.Uint64(uint64(t.Aux))
		e.writeTypRef(w, e.eir.Operands[t.FirstOp])
	case ergoir.HTMap:
		w.Code(pkgbits.TypeMap)
		e.writeTypRef(w, e.eir.Operands[t.FirstOp])
		e.writeTypRef(w, e.eir.Operands[t.FirstOp+1])
	case ergoir.HTSignature:
		w.Code(pkgbits.TypeSignature)
		e.writeSignature(w, typeID, 0)
	case ergoir.HTRecord:
		w.Code(pkgbits.TypeStruct)
		w.Len(int(t.OpCount))
		for j := uint32(0); j < t.OpCount; j++ {
			fsymID := e.eir.Operands[t.FirstOp+j]
			fsym := e.eir.Symbols[fsymID-1]
			e.writePos(w, fsym.Span)
			e.writeSelector(w, e.pkgIdxs[fsym.Import], memberShortName(fsym.Name))
			e.writeTypRef(w, fsym.Type)
			w.String("")  // tag
			w.Bool(false) // embedded
		}
	case ergoir.HTInterface:
		w.Code(pkgbits.TypeInterface)
		w.Len(int(t.OpCount))
		w.Len(0) // embeddeds
		for j := uint32(0); j < t.OpCount; j++ {
			msymID := e.eir.Operands[t.FirstOp+j]
			msym := e.eir.Symbols[msymID-1]
			e.writePos(w, msym.Span)
			e.writeSelector(w, e.pkgIdxs[msym.Import], memberShortName(msym.Name))
			e.writeSignature(w, msym.Type, msym.Span)
		}
	case ergoir.HTNamed:
		w.Code(pkgbits.TypeNamed)
		e.writeObjRef(w, e.encodeObj(t.Aux), nil)
	case ergoir.HTApplied:
		w.Code(pkgbits.TypeNamed)
		targs := make([]uint32, t.OpCount)
		for j := uint32(0); j < t.OpCount; j++ {
			targs[j] = e.eir.Operands[t.FirstOp+j]
		}
		e.writeObjRef(w, e.encodeObj(t.Aux), targs)
	default:
		e.failf("unsupported type kind %d in type %d", t.Kind, typeID)
	}

	return w.Flush()
}

func (e *ergoStubEncoder) writeSignature(w *pkgbits.Encoder, sigID uint32, spanID uint32) {
	sig := e.eir.Types[sigID-1]
	nparam := sig.Aux
	nresult := sig.OpCount - nparam

	w.Sync(pkgbits.SyncSignature)

	w.Sync(pkgbits.SyncParams)
	w.Len(int(nparam))
	for j := uint32(0); j < nparam; j++ {
		w.Sync(pkgbits.SyncParam)
		e.writePos(w, spanID)
		e.writeLocalIdent(w, e.pkgIdxs[0], fmt.Sprintf("p%d", j))
		e.writeTypRef(w, e.eir.Operands[sig.FirstOp+j])
	}

	w.Sync(pkgbits.SyncParams)
	w.Len(int(nresult))
	for j := uint32(0); j < nresult; j++ {
		w.Sync(pkgbits.SyncParam)
		e.writePos(w, spanID)
		e.writeLocalIdent(w, e.pkgIdxs[0], "")
		e.writeTypRef(w, e.eir.Operands[sig.FirstOp+nparam+j])
	}

	w.Bool(false) // variadic
}

func (e *ergoStubEncoder) writeObjRef(w *pkgbits.Encoder, objIdx index, explicitTypes []uint32) {
	w.Sync(pkgbits.SyncObject)
	if w.Version().Has(pkgbits.DerivedFuncInstance) {
		w.Bool(false)
	}
	w.Reloc(pkgbits.SectionObj, objIdx)
	w.Len(len(explicitTypes))
	for _, tid := range explicitTypes {
		e.writeTypRef(w, tid)
	}
}

func (e *ergoStubEncoder) writePkgObjs(w *pkgbits.Encoder, symID uint32) {
	w.Sync(pkgbits.SyncDeclNames)
	w.Len(1)
	w.Sync(pkgbits.SyncDeclName)
	e.writeObjRef(w, e.encodeObj(symID), nil)
}

func (e *ergoStubEncoder) writeEmptyObjDict(w *pkgbits.Encoder) {
	w.Len(0) // implicits
	if w.Version().Has(pkgbits.GenericMethods) {
		w.Len(0) // receivers
	}
	w.Len(0) // explicits
	w.Len(0) // derived
	w.Len(0) // typeParamMethodExprs
	w.Len(0) // subdicts
	w.Len(0) // rtypes
	w.Len(0) // itabs
}

func (e *ergoStubEncoder) writeLinkname(w *pkgbits.Encoder) {
	w.Sync(pkgbits.SyncLinkname)
	w.Int64(-1)
	w.String("")
	w.Bool(false)
}

func (e *ergoStubEncoder) writePragma(w *pkgbits.Encoder, p ir.PragmaFlag) {
	w.Sync(pkgbits.SyncPragma)
	w.Int(int(p))
}

func (e *ergoStubEncoder) encodeObj(symID uint32) index {
	if existing := e.objIdxs[symID-1]; existing != 0 {
		return existing - 1
	}

	sym := e.eir.Symbols[symID-1]

	w := e.NewEncoder(pkgbits.SectionObj, pkgbits.SyncObject1)
	wext := e.NewEncoder(pkgbits.SectionObjExt, pkgbits.SyncObject1)
	wname := e.NewEncoder(pkgbits.SectionName, pkgbits.SyncObject1)
	wdict := e.NewEncoder(pkgbits.SectionObjDict, pkgbits.SyncObject1)

	assert(wext.Idx == w.Idx)
	assert(wname.Idx == w.Idx)
	assert(wdict.Idx == w.Idx)
	e.objIdxs[symID-1] = w.Idx + 1

	var code pkgbits.CodeObj
	if sym.Import != 0 {
		code = pkgbits.ObjStub
	} else {
		switch sym.Kind {
		case ergoir.HSType:
			code = pkgbits.ObjType
			named := e.eir.Types[sym.Type-1]
			underlyingID := e.eir.Operands[named.FirstOp]
			if e.eir.Types[underlyingID-1].Kind == ergoir.HTGeneric {
				e.failf("local generic type %q not supported in v1 ingress", sym.Name)
			}
			e.writePos(w, sym.Span)
			w.Sync(pkgbits.SyncTypeParamNames)
			e.writeTypRef(w, underlyingID)
			w.Len(0) // methods
			if w.Version().Has(pkgbits.GenericMethods) {
				w.Len(0) // gmethods
			}

			wext.Sync(pkgbits.SyncTypeExt)
			e.writePragma(wext, 0)
			wext.Int64(-1)
			wext.Int64(-1)

		case ergoir.HSVar:
			code = pkgbits.ObjVar
			e.writePos(w, sym.Span)
			e.writeTypRef(w, sym.Type)

			wext.Sync(pkgbits.SyncVarExt)
			e.writeLinkname(wext)

		case ergoir.HSFunc:
			code = pkgbits.ObjFunc
			if e.eir.Types[sym.Type-1].Kind == ergoir.HTGeneric {
				e.failf("local generic function %q not supported in v1 ingress", sym.Name)
			}
			bodyIdx, ok := e.bodyOfSym[symID]
			if !ok {
				e.failf("missing body for local function %q", sym.Name)
			}
			e.writePos(w, sym.Span)
			if w.Version().Has(pkgbits.GenericMethods) {
				w.Bool(false) // not generic method
			}
			w.Sync(pkgbits.SyncTypeParamNames)
			e.writeSignature(w, sym.Type, sym.Span)
			e.writePos(w, sym.Span)

			bReloc := e.encodeBody(symID, bodyIdx)

			wext.Sync(pkgbits.SyncFuncExt)
			e.writePragma(wext, 0)
			e.writeLinkname(wext)
			if buildcfg.GOARCH == "wasm" {
				wext.String("")
				wext.String("")
				wext.String("")
			}
			wext.Bool(false) // local body
			wext.Reloc(pkgbits.SectionBody, bReloc)
			wext.Sync(pkgbits.SyncEOF)

		default:
			e.failf("unsupported local symbol kind %d for %q", sym.Kind, sym.Name)
		}
	}

	w.Flush()
	wext.Flush()

	e.writeQualifiedIdent(wname, e.pkgIdxs[sym.Import], sym.Name)
	wname.Code(code)
	wname.Flush()

	e.writeEmptyObjDict(wdict)
	wdict.Flush()

	return w.Idx
}

func (e *ergoStubEncoder) addLocal(w *pkgbits.Encoder, idx int) {
	w.Sync(pkgbits.SyncAddLocal)
	if e.SyncMarkers() {
		w.Int(idx)
	}
	w.Bool(false) // not derived dictIndex
}

func (e *ergoStubEncoder) useLocal(w *pkgbits.Encoder, localIdx int) {
	w.Sync(pkgbits.SyncUseObjLocal)
	w.Bool(true) // local (not closure var)
	w.Len(localIdx)
}

func (e *ergoStubEncoder) writeLocalExpr(w *pkgbits.Encoder, localIdx int) {
	w.Code(exprLocal)
	e.useLocal(w, localIdx)
}

func (e *ergoStubEncoder) writeOp(w *pkgbits.Encoder, op ir.Op) {
	w.Sync(pkgbits.SyncOp)
	w.Len(int(op))
}

func (e *ergoStubEncoder) writeLabel(w *pkgbits.Encoder, name string) {
	w.Sync(pkgbits.SyncLabel)
	w.String(name)
}

func (e *ergoStubEncoder) writeOptLabel(w *pkgbits.Encoder, name string) {
	w.Sync(pkgbits.SyncOptLabel)
	w.Bool(true)
	e.writeLabel(w, name)
}

func (e *ergoStubEncoder) writeRType(w *pkgbits.Encoder, typeID uint32) {
	w.Sync(pkgbits.SyncRType)
	w.Bool(false) // not derived
	e.writeTypRef(w, typeID)
}

func (e *ergoStubEncoder) writeConvRTTI(w *pkgbits.Encoder, srcTypeID, dstTypeID uint32) {
	w.Sync(pkgbits.SyncConvRTTI)
	e.writeRType(w, srcTypeID)
	e.writeRType(w, dstTypeID)
	w.Bool(false) // not derived itab
}

func (e *ergoStubEncoder) encodeBody(symID uint32, bodyIdx int) index {
	sym := e.eir.Symbols[symID-1]
	sig := e.eir.Types[sym.Type-1]
	nparam := sig.Aux
	nresult := sig.OpCount - nparam
	b := e.eir.Bodies[bodyIdx]

	w := e.NewEncoder(pkgbits.SectionBody, pkgbits.SyncFuncBody)

	// Match reader.declareParams(): params then unnamed results.
	nextLocal := 0
	for j := uint32(0); j < nparam; j++ {
		e.addLocal(w, nextLocal)
		nextLocal++
	}
	for j := uint32(0); j < nresult; j++ {
		e.addLocal(w, nextLocal)
		nextLocal++
	}

	slotLocal := func(slot uint32) int {
		if slot <= nparam {
			return int(slot - 1)
		}
		return int(nresult) + int(slot-1)
	}
	slotTypeID := func(slot uint32) uint32 {
		return e.eir.Slots[b.FirstSlot+slot-1]
	}

	w.Bool(true) // has body
	w.Sync(pkgbits.SyncStmts)

	// Declare every non-parameter local slot up front so goto never jumps over declarations.
	for s := nparam + 1; s <= b.SlotCount; s++ {
		w.Code(stmtAssign)
		e.writePos(w, sym.Span)
		w.Len(1)
		w.Code(assignDef)
		e.writePos(w, sym.Span)
		e.writeLocalIdent(w, e.pkgIdxs[0], fmt.Sprintf("s%d", s))
		e.writeTypRef(w, slotTypeID(s))
		e.addLocal(w, nextLocal)
		nextLocal++
		// Empty rhs -> zero-initialized declaration.
		w.Sync(pkgbits.SyncMultiExpr)
		w.Bool(false)
		w.Len(0)
	}

	// Mark jump targets within this body (1-based op index -> 0-based).
	isTarget := make([]bool, b.OpCount)
	for i := uint32(0); i < b.OpCount; i++ {
		op := e.eir.Ops[b.FirstOp+i]
		switch op.Opcode {
		case ergoir.HOJump:
			t := e.eir.Operands[op.FirstOp]
			isTarget[t-1] = true
		case ergoir.HOBranch:
			yesT := e.eir.Operands[op.FirstOp+3]
			noT := e.eir.Operands[op.FirstOp+4]
			isTarget[yesT-1] = true
			isTarget[noT-1] = true
		}
	}

	var lastSpan uint32 = sym.Span
	for i := uint32(0); i < b.OpCount; i++ {
		op := e.eir.Ops[b.FirstOp+i]
		lastSpan = op.Span
		if isTarget[i] {
			w.Code(stmtLabel)
			e.writePos(w, op.Span)
			e.writeLabel(w, fmt.Sprintf("L%d", i+1))
		}
		e.encodeOp(w, op, slotLocal, slotTypeID)
	}

	w.Code(stmtEnd)
	w.Sync(pkgbits.SyncStmtsEnd)
	e.writePos(w, lastSpan)

	return w.Flush()
}

func (e *ergoStubEncoder) encodeOp(
	w *pkgbits.Encoder,
	op ergoir.EIROp,
	slotLocal func(uint32) int,
	slotTypeID func(uint32) uint32,
) {
	arg := func(j uint32) uint32 { return e.eir.Operands[op.FirstOp+j] }

	writeAssign1 := func(lhs func(), rhs func()) {
		w.Code(stmtAssign)
		e.writePos(w, op.Span)
		w.Len(1)
		w.Code(assignExpr)
		lhs()
		w.Sync(pkgbits.SyncMultiExpr)
		w.Bool(false)
		w.Len(1)
		rhs()
	}

	switch op.Opcode {
	case ergoir.HOConst:
		dst := arg(0)
		lo := arg(1)
		hi := arg(2)
		tid := slotTypeID(dst)
		writeAssign1(
			func() { e.writeLocalExpr(w, slotLocal(dst)) },
			func() { e.writeConstExpr(w, op.Span, tid, lo, hi) },
		)

	case ergoir.HOAdd, ergoir.HOSub, ergoir.HOMul, ergoir.HOXor:
		dst, lhs, rhs := arg(0), arg(1), arg(2)
		var binOp ir.Op
		switch op.Opcode {
		case ergoir.HOAdd:
			binOp = ir.OADD
		case ergoir.HOSub:
			binOp = ir.OSUB
		case ergoir.HOMul:
			binOp = ir.OMUL
		case ergoir.HOXor:
			binOp = ir.OXOR
		}
		writeAssign1(
			func() { e.writeLocalExpr(w, slotLocal(dst)) },
			func() {
				w.Code(exprBinaryOp)
				e.writeOp(w, binOp)
				e.writeLocalExpr(w, slotLocal(lhs))
				e.writePos(w, op.Span)
				e.writeLocalExpr(w, slotLocal(rhs))
			},
		)

	case ergoir.HOLoad:
		dst, ptr := arg(0), arg(1)
		writeAssign1(
			func() { e.writeLocalExpr(w, slotLocal(dst)) },
			func() {
				w.Code(exprUnaryOp)
				e.writeOp(w, ir.ODEREF)
				e.writePos(w, op.Span)
				e.writeLocalExpr(w, slotLocal(ptr))
			},
		)

	case ergoir.HOStore:
		ptr, val := arg(0), arg(1)
		writeAssign1(
			func() {
				w.Code(exprUnaryOp)
				e.writeOp(w, ir.ODEREF)
				e.writePos(w, op.Span)
				e.writeLocalExpr(w, slotLocal(ptr))
			},
			func() { e.writeLocalExpr(w, slotLocal(val)) },
		)

	case ergoir.HOAddrOf:
		dst, src := arg(0), arg(1)
		writeAssign1(
			func() { e.writeLocalExpr(w, slotLocal(dst)) },
			func() {
				w.Code(exprUnaryOp)
				e.writeOp(w, ir.OADDR)
				e.writePos(w, op.Span)
				e.writeLocalExpr(w, slotLocal(src))
			},
		)

	case ergoir.HOGetGlobal:
		dst, symID := arg(0), arg(1)
		writeAssign1(
			func() { e.writeLocalExpr(w, slotLocal(dst)) },
			func() {
				w.Code(exprGlobal)
				e.writeObjRef(w, e.encodeObj(symID), nil)
			},
		)

	case ergoir.HOSetGlobal:
		symID, src := arg(0), arg(1)
		writeAssign1(
			func() {
				w.Code(exprGlobal)
				e.writeObjRef(w, e.encodeObj(symID), nil)
			},
			func() { e.writeLocalExpr(w, slotLocal(src)) },
		)

	case ergoir.HOCall, ergoir.HOCallClosure, ergoir.HOCallInstance:
		target := arg(0)
		nresult := arg(1)
		narg := op.OpCount - 2 - nresult

		writeCallExpr := func() {
			w.Code(exprCall)
			w.Bool(false) // not method call
			switch op.Opcode {
			case ergoir.HOCallInstance:
				inst := e.eir.Instances[target-1]
				targs := make([]uint32, inst.OpCount)
				for j := uint32(0); j < inst.OpCount; j++ {
					targs[j] = e.eir.Operands[inst.FirstOp+j]
				}
				w.Bool(true) // call to instanced function
				e.writePos(w, op.Span)
				w.Bool(false) // static dictionary
				e.writeObjRef(w, e.encodeObj(inst.GenericSym), targs)
			case ergoir.HOCall:
				w.Bool(false)
				w.Code(exprGlobal)
				e.writeObjRef(w, e.encodeObj(target), nil)
			case ergoir.HOCallClosure:
				w.Bool(false)
				e.writeLocalExpr(w, slotLocal(target))
			}
			e.writePos(w, op.Span)
			w.Sync(pkgbits.SyncMultiExpr)
			w.Bool(false) // N:N args
			w.Len(int(narg))
			for j := uint32(0); j < narg; j++ {
				e.writeLocalExpr(w, slotLocal(arg(2+nresult+j)))
			}
			w.Bool(false) // dots
		}

		if nresult == 0 {
			w.Code(stmtExpr)
			writeCallExpr()
		} else if nresult == 1 {
			writeAssign1(
				func() { e.writeLocalExpr(w, slotLocal(arg(2))) },
				writeCallExpr,
			)
		} else {
			w.Code(stmtAssign)
			e.writePos(w, op.Span)
			w.Len(int(nresult))
			for j := uint32(0); j < nresult; j++ {
				w.Code(assignExpr)
				e.writeLocalExpr(w, slotLocal(arg(2+j)))
			}
			w.Sync(pkgbits.SyncMultiExpr)
			w.Bool(true) // N:1
			e.writePos(w, op.Span)
			writeCallExpr()
			w.Len(int(nresult))
			for j := uint32(0); j < nresult; j++ {
				e.writeTypRef(w, slotTypeID(arg(2+j)))
				w.Bool(false) // no implicit conversion
			}
		}

	case ergoir.HOJump:
		target := arg(0)
		w.Code(stmtBranch)
		e.writePos(w, op.Span)
		e.writeOp(w, ir.OGOTO)
		e.writeOptLabel(w, fmt.Sprintf("L%d", target))

	case ergoir.HOBranch:
		cond, lhs, rhs, yesT, noT := arg(0), arg(1), arg(2), arg(3), arg(4)
		var cmpOp ir.Op
		switch cond {
		case ergoir.HCLess:
			cmpOp = ir.OLT
		case ergoir.HCNotEqual:
			cmpOp = ir.ONE
		case ergoir.HCEqual:
			cmpOp = ir.OEQ
		case ergoir.HCAtLeast:
			cmpOp = ir.OGE
		}
		w.Code(stmtIf)
		w.Sync(pkgbits.SyncIfStmt)
		w.Sync(pkgbits.SyncOpenScope)
		e.writePos(w, op.Span)
		e.writePos(w, op.Span)
		// Empty init stmts.
		w.Sync(pkgbits.SyncStmts)
		w.Code(stmtEnd)
		w.Sync(pkgbits.SyncStmtsEnd)
		// Cond expr.
		w.Code(exprBinaryOp)
		e.writeOp(w, cmpOp)
		e.writeLocalExpr(w, slotLocal(lhs))
		e.writePos(w, op.Span)
		e.writeLocalExpr(w, slotLocal(rhs))
		// Dynamic branch.
		w.Int(0)
		// Then block: goto L<yesT>.
		w.Sync(pkgbits.SyncBlockStmt)
		w.Sync(pkgbits.SyncOpenScope)
		e.writePos(w, op.Span)
		w.Sync(pkgbits.SyncStmts)
		w.Code(stmtBranch)
		e.writePos(w, op.Span)
		e.writeOp(w, ir.OGOTO)
		e.writeOptLabel(w, fmt.Sprintf("L%d", yesT))
		w.Code(stmtEnd)
		w.Sync(pkgbits.SyncStmtsEnd)
		w.Sync(pkgbits.SyncCloseScope)
		e.writePos(w, op.Span)
		w.Sync(pkgbits.SyncCloseAnotherScope)
		// Else stmts: goto L<noT>.
		w.Sync(pkgbits.SyncStmts)
		w.Code(stmtBranch)
		e.writePos(w, op.Span)
		e.writeOp(w, ir.OGOTO)
		e.writeOptLabel(w, fmt.Sprintf("L%d", noT))
		w.Code(stmtEnd)
		w.Sync(pkgbits.SyncStmtsEnd)
		// Close if scope.
		w.Sync(pkgbits.SyncCloseAnotherScope)

	case ergoir.HOReturn:
		w.Code(stmtReturn)
		e.writePos(w, op.Span)
		w.Sync(pkgbits.SyncMultiExpr)
		w.Bool(false) // N:N
		w.Len(int(op.OpCount))
		for j := uint32(0); j < op.OpCount; j++ {
			e.writeLocalExpr(w, slotLocal(arg(j)))
		}

	case ergoir.HOConvert:
		dst, src := arg(0), arg(1)
		dstTID := slotTypeID(dst)
		srcTID := slotTypeID(src)
		writeAssign1(
			func() { e.writeLocalExpr(w, slotLocal(dst)) },
			func() {
				w.Code(exprConvert)
				w.Bool(false) // explicit
				e.writeTypRef(w, dstTID)
				e.writePos(w, op.Span)
				e.writeConvRTTI(w, srcTID, dstTID)
				w.Bool(false) // dstTypeParam
				w.Bool(dstTID == srcTID)
				e.writeLocalExpr(w, slotLocal(src))
			},
		)

	default:
		e.failf("unsupported opcode %d", op.Opcode)
	}
}

func (e *ergoStubEncoder) writeConstExpr(w *pkgbits.Encoder, spanID, typeID, lo, hi uint32) {
	t := e.eir.Types[typeID-1]
	raw := uint64(lo) | uint64(hi)<<32
	if t.Kind != ergoir.HTBasic {
		if raw == 0 {
			w.Code(exprZero)
			e.writePos(w, spanID)
			e.writeTypRef(w, typeID)
			return
		}
		e.failf("non-zero HOConst on non-basic type %d", typeID)
		return
	}
	if t.Aux == ergoir.HBasicAny {
		if raw == 0 {
			w.Code(exprZero)
			e.writePos(w, spanID)
			e.writeTypRef(w, typeID)
			return
		}
		e.failf("non-zero HOConst on any type %d", typeID)
		return
	}

	var val constant.Value
	switch t.Aux {
	case ergoir.HBasicBool:
		val = constant.MakeBool(raw != 0)
	case ergoir.HBasicInt, ergoir.HBasicInt8, ergoir.HBasicInt16, ergoir.HBasicInt32, ergoir.HBasicInt64:
		val = constant.MakeInt64(int64(raw))
	case ergoir.HBasicUint, ergoir.HBasicUint8, ergoir.HBasicUint16, ergoir.HBasicUint32, ergoir.HBasicUint64, ergoir.HBasicUintptr:
		val = constant.MakeUint64(raw)
	case ergoir.HBasicFloat32:
		val = constant.MakeFloat64(float64(math.Float32frombits(lo)))
	case ergoir.HBasicFloat64:
		val = constant.MakeFloat64(math.Float64frombits(raw))
	case ergoir.HBasicString:
		if lo == 0 && hi == 0 {
			val = constant.MakeString("")
		} else if hi == 0 && lo <= uint32(len(e.eir.Names)) {
			val = constant.MakeString(e.eir.Names[lo-1])
		} else {
			e.failf("invalid string HOConst (%d, %d)", lo, hi)
			return
		}
	default:
		if raw == 0 {
			w.Code(exprZero)
			e.writePos(w, spanID)
			e.writeTypRef(w, typeID)
			return
		}
		e.failf("unsupported scalar HOConst %d", t.Aux)
		return
	}

	w.Code(exprConst)
	e.writePos(w, spanID)
	e.writeTypRef(w, typeID)
	w.Value(val)
}
