package ergoir

import (
	"encoding/binary"
	"fmt"
	"strings"
)

const (
	EIRMagic    = "ERGOEIR\n"
	EIRVersion  = uint32(1)
	EIRMaxBytes = 256 * 1024 * 1024
	EIRSections = 16
)

const (
	HPackage   = 0
	HNames     = 1
	HImports   = 2
	HFiles     = 3
	HLines     = 4
	HSpans     = 5
	HTypes     = 6
	HSymbols   = 7
	HBodies    = 8
	HSlots     = 9
	HOps       = 10
	HInstances = 11
	HFacts     = 12
	HInit      = 13
	HOperands  = 14
	HText      = 15
)

const (
	HTBasic     = uint32(1)
	HTPointer   = uint32(2)
	HTSignature = uint32(3)
	HTRecord    = uint32(4)
	HTNamed     = uint32(5)
	HTParameter = uint32(6)
	HTGeneric   = uint32(7)
	HTApplied   = uint32(8)
	HTSlice     = uint32(9)
	HTArray     = uint32(10)
	HTMap       = uint32(11)
	HTInterface = uint32(12)

	HBasicBool       = uint32(1)
	HBasicInt        = uint32(2)
	HBasicInt8       = uint32(3)
	HBasicInt16      = uint32(4)
	HBasicInt32      = uint32(5)
	HBasicInt64      = uint32(6)
	HBasicUint       = uint32(7)
	HBasicUint8      = uint32(8)
	HBasicUint16     = uint32(9)
	HBasicUint32     = uint32(10)
	HBasicUint64     = uint32(11)
	HBasicUintptr    = uint32(12)
	HBasicFloat32    = uint32(13)
	HBasicFloat64    = uint32(14)
	HBasicComplex64  = uint32(15)
	HBasicComplex128 = uint32(16)
	HBasicString     = uint32(17)
	HBasicAny        = uint32(18)

	HSFunc       = uint32(1)
	HSVar        = uint32(2)
	HSType       = uint32(3)
	HSField      = uint32(4)
	HSDictionary = uint32(5)
	HSMethod     = uint32(6)

	HOConst        = uint32(1)
	HOAdd          = uint32(2)
	HOSub          = uint32(3)
	HOMul          = uint32(4)
	HOXor          = uint32(5)
	HOLoad         = uint32(6)
	HOStore        = uint32(7)
	HOAddrOf       = uint32(8)
	HOGetGlobal    = uint32(9)
	HOSetGlobal    = uint32(10)
	HOCall         = uint32(11)
	HOCallClosure  = uint32(12)
	HOJump         = uint32(13)
	HOBranch       = uint32(14)
	HOReturn       = uint32(15)
	HOCallInstance = uint32(16)
	HOConvert      = uint32(17)

	HCLess     = uint32(1)
	HCNotEqual = uint32(2)
	HCEqual    = uint32(3)
	HCAtLeast  = uint32(4)

	HFBounds = uint32(1)
)

var eirWidths = [EIRSections]int{
	HPackage:   6,
	HNames:     1,
	HImports:   5,
	HFiles:     12,
	HLines:     1,
	HSpans:     3,
	HTypes:     4,
	HSymbols:   5,
	HBodies:    5,
	HSlots:     1,
	HOps:       4,
	HInstances: 5,
	HFacts:     5,
	HInit:      1,
	HOperands:  1,
	HText:      1,
}

type EIRTarget struct {
	Path    string
	GOOS    string
	GOARCH  string
	Pin     string
	Profile string
}

type EIRImport struct {
	Path        string
	Name        string
	HasInit     bool
	Fingerprint [8]byte
}

type EIRFile struct {
	Path      string
	ByteSize  uint32
	FirstLine uint32
	LineCount uint32
	Rev       [8]uint32
}

type EIRSpan struct {
	File  uint32 // 1-based file index
	Start uint32
	End   uint32
}

type EIRType struct {
	Kind    uint32
	Aux     uint32
	FirstOp uint32
	OpCount uint32
}

type EIRSymbol struct {
	Import uint32 // 0 = local, 1.. = HImports index
	Name   string
	Kind   uint32
	Type   uint32 // 1-based HTypes index
	Span   uint32 // 1-based HSpans index (0 for foreign)
}

type EIRBody struct {
	FuncSym   uint32 // 1-based HSymbols index
	FirstSlot uint32
	SlotCount uint32
	FirstOp   uint32
	OpCount   uint32
}

type EIROp struct {
	Opcode  uint32
	Span    uint32
	FirstOp uint32
	OpCount uint32
}

type EIRInstance struct {
	GenericSym     uint32
	SpecializedSig uint32
	DictSym        uint32
	FirstOp        uint32
	OpCount        uint32
}

type EIRFact struct {
	Op          uint32
	Kind        uint32
	SubjectSlot uint32
	LimitSlot   uint32
	ProofOp     uint32
}

type EIRPackage struct {
	Target    EIRTarget
	PkgName   string
	Names     []string
	Imports   []EIRImport
	Files     []EIRFile
	Lines     []uint32
	Spans     []EIRSpan
	Types     []EIRType
	Symbols   []EIRSymbol
	Bodies    []EIRBody
	Slots     []uint32
	Ops       []EIROp
	Instances []EIRInstance
	Facts     []EIRFact
	Inits     []uint32 // 1-based HSymbols indices in semantic order
	Operands  []uint32

	raw   []byte
	dir   [EIRSections]eirSection
	names []uint32
}

type eirSection struct {
	start int
	count uint32
}

func (p *EIRPackage) rows(s int) int { return int(p.dir[s].count) }

func (p *EIRPackage) word(s, row, col int) uint32 {
	at := p.dir[s].start + (p.rows(s)*col+row)*4
	return binary.LittleEndian.Uint32(p.raw[at : at+4])
}

func (p *EIRPackage) ref(s int, id uint32) bool {
	return id > 0 && id <= p.dir[s].count
}

func (p *EIRPackage) run(s int, at, first, n uint32) bool {
	return first == at && n <= p.dir[s].count-at
}

func (p *EIRPackage) operand(lo, off uint32) uint32 {
	return p.word(HOperands, int(lo+off), 0)
}

func (p *EIRPackage) nameOf(id uint32) string {
	return p.Names[id-1]
}

// SpanPos resolves a 1-based span ID to a 0-based file index and 1-based (line, col).
func (p *EIRPackage) SpanPos(spanID uint32) (fileIdx int, line, col uint) {
	sp := p.Spans[spanID-1]
	fileIdx = int(sp.File) - 1
	f := p.Files[fileIdx]
	base := int(f.FirstLine)
	lo, hi := base, base+int(f.LineCount)
	for lo+1 < hi {
		mid := lo + (hi-lo)/2
		if p.Lines[mid] <= sp.Start {
			lo = mid
		} else {
			hi = mid
		}
	}
	line = uint(lo-base) + 1
	col = uint(sp.Start-p.Lines[lo]) + 1
	return fileIdx, line, col
}

// DecodeEIR decodes and structurally validates a frozen EIR v1 image fail-closed.
func DecodeEIR(wire []byte) (*EIRPackage, error) {
	const headerBytes = 8 + 4 + EIRSections*4
	if len(wire) > EIRMaxBytes {
		return nil, fmt.Errorf("ergopkg: image size %d exceeds limit %d", len(wire), EIRMaxBytes)
	}
	if len(wire) < headerBytes || string(wire[:8]) != EIRMagic {
		return nil, fmt.Errorf("ergopkg: missing or invalid EIR magic")
	}
	ver := binary.LittleEndian.Uint32(wire[8:12])
	if ver != EIRVersion {
		return nil, fmt.Errorf("ergopkg: unsupported EIR schema version %d (want %d)", ver, EIRVersion)
	}

	p := &EIRPackage{raw: wire}
	at := headerBytes
	for i := 0; i < EIRSections; i++ {
		n := binary.LittleEndian.Uint32(wire[12+i*4 : 16+i*4])
		width := eirWidths[i]
		if i != HText {
			width *= 4
		}
		if n > uint32((len(wire)-at)/width) {
			return nil, fmt.Errorf("ergopkg: section %d count %d exceeds image bounds", i, n)
		}
		p.dir[i] = eirSection{start: at, count: n}
		at += int(n) * width
	}
	if at != len(wire) {
		return nil, fmt.Errorf("ergopkg: trailing bytes after HText (%d != %d)", at, len(wire))
	}
	if p.dir[HPackage].count != 1 {
		return nil, fmt.Errorf("ergopkg: HPackage must have exactly 1 row (got %d)", p.dir[HPackage].count)
	}

	if err := p.validateNamesAndImports(); err != nil {
		return nil, err
	}
	if err := p.validateSources(); err != nil {
		return nil, err
	}
	if err := p.validateSymbols(); err != nil {
		return nil, err
	}
	opStart, err := p.validateTypes()
	if err != nil {
		return nil, err
	}
	if err := p.validateDeclarations(); err != nil {
		return nil, err
	}
	if err := p.validateStorageGraph(); err != nil {
		return nil, err
	}
	instStart, err := p.validateBodies(opStart)
	if err != nil {
		return nil, err
	}
	if err := p.validateInstances(instStart); err != nil {
		return nil, err
	}
	if err := p.validateInitOrder(); err != nil {
		return nil, err
	}

	p.materialize()
	return p, nil
}

func (p *EIRPackage) validateNamesAndImports() error {
	textStart := p.dir[HText].start
	textLen := p.dir[HText].count
	nNames := p.rows(HNames)
	p.Names = make([]string, nNames)
	p.names = make([]uint32, nNames)
	var end uint32
	prev := ""
	for i := 0; i < nNames; i++ {
		next := p.word(HNames, i, 0)
		if next <= end || next > textLen {
			return fmt.Errorf("ergopkg: HNames[%d] bounds invalid (%d..%d, text=%d)", i, end, next, textLen)
		}
		s := string(p.raw[textStart+int(end) : textStart+int(next)])
		if s <= prev || strings.ContainsRune(s, 0) {
			return fmt.Errorf("ergopkg: HNames[%d] not strictly sorted non-NUL string", i)
		}
		p.Names[i] = s
		p.names[i] = next
		prev = s
		end = next
	}
	if end != textLen {
		return fmt.Errorf("ergopkg: HText has unowned trailing bytes (%d != %d)", end, textLen)
	}
	for c := 0; c < eirWidths[HPackage]; c++ {
		if !p.ref(HNames, p.word(HPackage, 0, c)) {
			return fmt.Errorf("ergopkg: HPackage column %d invalid name ref", c)
		}
	}
	pkgPathID := p.word(HPackage, 0, 0)
	var lastPath uint32
	for i := 0; i < p.rows(HImports); i++ {
		next := p.word(HImports, i, 0)
		pkgName := p.word(HImports, i, 1)
		hasInit := p.word(HImports, i, 2)
		if next <= lastPath || !p.ref(HNames, next) || next == pkgPathID || !p.ref(HNames, pkgName) || hasInit > 1 {
			return fmt.Errorf("ergopkg: HImports[%d] invalid identity or order", i)
		}
		lastPath = next
	}
	return nil
}

func (p *EIRPackage) validateSources() error {
	var lastPath, at uint32
	for i := 0; i < p.rows(HFiles); i++ {
		next := p.word(HFiles, i, 0)
		size := p.word(HFiles, i, 1)
		lo := p.word(HFiles, i, 2)
		n := p.word(HFiles, i, 3)
		if next <= lastPath || !p.ref(HNames, next) || !strings.HasSuffix(p.nameOf(next), ".ergo") {
			return fmt.Errorf("ergopkg: HFiles[%d] invalid .ergo path or order", i)
		}
		if size == 0xffffffff || n == 0 || !p.run(HLines, at, lo, n) || p.word(HLines, int(lo), 0) != 0 {
			return fmt.Errorf("ergopkg: HFiles[%d] invalid line run", i)
		}
		var last uint32
		for j := int(lo) + 1; j < int(lo+n); j++ {
			x := p.word(HLines, j, 0)
			if x <= last || x > size {
				return fmt.Errorf("ergopkg: HLines[%d] outside file or out of order", j)
			}
			last = x
		}
		lastPath = next
		at = lo + n
	}
	if at != p.dir[HLines].count {
		return fmt.Errorf("ergopkg: HLines has unowned rows")
	}
	for i := 0; i < p.rows(HSpans); i++ {
		f := p.word(HSpans, i, 0)
		start := p.word(HSpans, i, 1)
		end := p.word(HSpans, i, 2)
		if !p.ref(HFiles, f) || start > end || end > p.word(HFiles, int(f)-1, 1) {
			return fmt.Errorf("ergopkg: HSpans[%d] outside source file", i)
		}
	}
	return nil
}

func (p *EIRPackage) symKind(id, k uint32) bool {
	return p.ref(HSymbols, id) && p.word(HSymbols, int(id)-1, 2) == k
}

func (p *EIRPackage) typeKind(t uint32) uint32 {
	return p.word(HTypes, int(t)-1, 0)
}

func (p *EIRPackage) validateSymbols() error {
	var imp, name uint32
	for i := 0; i < p.rows(HSymbols); i++ {
		x := p.word(HSymbols, i, 0)
		n := p.word(HSymbols, i, 1)
		k := p.word(HSymbols, i, 2)
		t := p.word(HSymbols, i, 3)
		span := p.word(HSymbols, i, 4)
		if x < imp || (x == imp && n <= name) || (x != 0 && !p.ref(HImports, x)) || !p.ref(HNames, n) ||
			k < HSFunc || k > HSMethod || !p.ref(HTypes, t) {
			return fmt.Errorf("ergopkg: HSymbols[%d] invalid identity, kind, type, or order", i)
		}
		if (x == 0 && !p.ref(HSpans, span)) || (x != 0 && span != 0) {
			return fmt.Errorf("ergopkg: HSymbols[%d] invalid local/foreign span", i)
		}
		imp = x
		name = n
	}
	return nil
}

func (p *EIRPackage) typeShape(k, x, n, t uint32) bool {
	switch k {
	case HTBasic:
		return n == 0 && x > 0 && x <= HBasicAny
	case HTPointer, HTSlice:
		return x == 0 && n == 1
	case HTArray:
		return n == 1
	case HTMap:
		return x == 0 && n == 2
	case HTSignature:
		return x <= n
	case HTRecord, HTInterface:
		return x == 0
	case HTNamed:
		return n == 1 && p.symKind(x, HSType) && p.word(HSymbols, int(x)-1, 3) == t
	case HTParameter:
		return n == 1 && (p.symKind(x, HSFunc) || p.symKind(x, HSType))
	case HTGeneric:
		return n > 0 && p.ref(HTypes, x) && x != t
	case HTApplied:
		return n > 0 && p.symKind(x, HSType)
	default:
		return false
	}
}

func (p *EIRPackage) typeOperand(k, t, x uint32) bool {
	switch k {
	case HTRecord:
		return p.symKind(x, HSField)
	case HTInterface:
		return p.symKind(x, HSMethod)
	case HTGeneric:
		return p.ref(HTypes, x) && p.typeKind(x) == HTParameter
	default:
		return p.ref(HTypes, x) && x != t
	}
}

func (p *EIRPackage) validateTypes() (uint32, error) {
	var at uint32
	for i := 0; i < p.rows(HTypes); i++ {
		k := p.word(HTypes, i, 0)
		x := p.word(HTypes, i, 1)
		lo := p.word(HTypes, i, 2)
		n := p.word(HTypes, i, 3)
		t := uint32(i + 1)
		if !p.typeShape(k, x, n, t) || !p.run(HOperands, at, lo, n) {
			return 0, fmt.Errorf("ergopkg: HTypes[%d] invalid kind, arity, or operand run", i)
		}
		for j := uint32(0); j < n; j++ {
			if !p.typeOperand(k, t, p.operand(lo, j)) {
				return 0, fmt.Errorf("ergopkg: HTypes[%d] invalid operand %d", i, j)
			}
		}
		at = lo + n
	}
	return at, nil
}

func (p *EIRPackage) genericType(sym uint32) uint32 {
	t := p.word(HSymbols, int(sym)-1, 3)
	if p.typeKind(t) == HTNamed {
		return p.operand(p.word(HTypes, int(t)-1, 2), 0)
	}
	return t
}

func (p *EIRPackage) signature(t uint32) uint32 {
	k := p.typeKind(t)
	sig := t
	if k == HTGeneric {
		sig = p.word(HTypes, int(t)-1, 1)
	} else if k == HTNamed {
		sig = p.operand(p.word(HTypes, int(t)-1, 2), 0)
	}
	if p.typeKind(sig) == HTSignature {
		return sig
	}
	return 0
}

func (p *EIRPackage) signatureSize(sig uint32) (uint32, uint32) {
	i := int(sig) - 1
	n := p.word(HTypes, i, 1)
	return n, p.word(HTypes, i, 3) - n
}

func (p *EIRPackage) signatureType(sig, j uint32) uint32 {
	return p.operand(p.word(HTypes, int(sig)-1, 2), j)
}

func (p *EIRPackage) validateDeclarations() error {
	n := p.rows(HTypes)
	owned := make([]bool, n)
	for i := 0; i < p.rows(HSymbols); i++ {
		k := p.word(HSymbols, i, 2)
		s := uint32(i + 1)
		base := p.word(HSymbols, i, 3)
		if k == HSType && (p.typeKind(base) != HTNamed || p.word(HTypes, int(base)-1, 1) != s) {
			return fmt.Errorf("ergopkg: HSymbols[%d] invalid named type identity", i)
		}
		if k != HSType && k != HSFunc && k != HSMethod {
			continue
		}
		t := p.genericType(s)
		if (k == HSFunc || k == HSMethod) && p.signature(t) == 0 {
			return fmt.Errorf("ergopkg: HSymbols[%d] callable declaration missing signature", i)
		}
		if p.typeKind(t) != HTGeneric {
			continue
		}
		row := int(t) - 1
		lo := p.word(HTypes, row, 2)
		cnt := p.word(HTypes, row, 3)
		for j := uint32(0); j < cnt; j++ {
			pidx := int(p.operand(lo, j)) - 1
			if owned[pidx] || p.word(HTypes, pidx, 1) != s {
				return fmt.Errorf("ergopkg: HTypes[%d] duplicate or foreign type parameter", pidx)
			}
			owned[pidx] = true
		}
	}
	for i := 0; i < n; i++ {
		k := p.word(HTypes, i, 0)
		if k == HTParameter && !owned[i] {
			return fmt.Errorf("ergopkg: HTypes[%d] unowned type parameter", i)
		}
		if k == HTApplied {
			g := p.genericType(p.word(HTypes, i, 1))
			if p.typeKind(g) != HTGeneric || p.word(HTypes, i, 3) != p.word(HTypes, int(g)-1, 3) {
				return fmt.Errorf("ergopkg: HTypes[%d] type application arity mismatch", i)
			}
		}
	}
	return nil
}

func (p *EIRPackage) embeds(i int) uint32 {
	k := p.word(HTypes, i, 0)
	if k == HTRecord {
		return p.word(HTypes, i, 3)
	}
	if k == HTNamed || k == HTArray || k == HTGeneric || k == HTApplied {
		return 1
	}
	return 0
}

func (p *EIRPackage) embeddedType(i int, j uint32) int {
	k := p.word(HTypes, i, 0)
	if k == HTGeneric {
		return int(p.word(HTypes, i, 1)) - 1
	}
	if k == HTApplied {
		return int(p.genericType(p.word(HTypes, i, 1))) - 1
	}
	x := p.operand(p.word(HTypes, i, 2), j)
	if k == HTRecord {
		return int(p.word(HSymbols, int(x)-1, 3)) - 1
	}
	return int(x) - 1
}

func (p *EIRPackage) validateStorageGraph() error {
	type frame struct {
		row  int
		next uint32
		n    uint32
	}
	n := p.rows(HTypes)
	state := make([]uint8, n)
	var todo []frame
	for i := 0; i < n; i++ {
		if state[i] != 0 {
			continue
		}
		state[i] = 1
		todo = append(todo, frame{row: i, next: 0, n: p.embeds(i)})
		for len(todo) > 0 {
			top := len(todo) - 1
			f := todo[top]
			if f.next == f.n {
				state[f.row] = 2
				todo = todo[:top]
				continue
			}
			x := p.embeddedType(f.row, f.next)
			todo[top].next++
			if state[x] == 1 {
				return fmt.Errorf("ergopkg: HTypes[%d] embeds itself by value", x)
			}
			if state[x] == 0 {
				state[x] = 1
				todo = append(todo, frame{row: x, next: 0, n: p.embeds(x)})
			}
		}
	}
	return nil
}

type bodyCtx struct {
	slots  uint32
	nslots uint32
	ops    uint32
	nops   uint32
	sig    uint32
}

func (p *EIRPackage) slotValid(b bodyCtx, s uint32) bool {
	return s > 0 && s <= b.nslots
}

func (p *EIRPackage) slotType(b bodyCtx, s uint32) uint32 {
	return p.word(HSlots, int(b.slots+s)-1, 0)
}

func (p *EIRPackage) matchesSlot(b bodyCtx, s, t uint32) bool {
	return p.slotValid(b, s) && p.slotType(b, s) == t
}

func (p *EIRPackage) callValid(b bodyCtx, k, lo, n uint32) bool {
	if n < 2 {
		return false
	}
	target := p.operand(lo, 0)
	var t uint32
	switch k {
	case HOCall:
		if !p.symKind(target, HSFunc) {
			return false
		}
		t = p.word(HSymbols, int(target)-1, 3)
		if p.typeKind(t) == HTGeneric {
			return false
		}
	case HOCallClosure:
		if !p.slotValid(b, target) {
			return false
		}
		t = p.slotType(b, target)
	default:
		if !p.ref(HInstances, target) {
			return false
		}
		t = p.word(HInstances, int(target)-1, 1)
		if !p.ref(HTypes, t) {
			return false
		}
	}
	sig := p.signature(t)
	if sig == 0 {
		return false
	}
	nparam, nresult := p.signatureSize(sig)
	if p.operand(lo, 1) != nresult || n-2 != nparam+nresult {
		return false
	}
	for j := uint32(0); j < nparam; j++ {
		if !p.matchesSlot(b, p.operand(lo, 2+nresult+j), p.signatureType(sig, j)) {
			return false
		}
	}
	for j := uint32(0); j < nresult; j++ {
		if !p.matchesSlot(b, p.operand(lo, 2+j), p.signatureType(sig, nparam+j)) {
			return false
		}
	}
	return true
}

func (p *EIRPackage) opValid(b bodyCtx, k, lo, n uint32) bool {
	switch k {
	case HOConst:
		return n == 3 && p.slotValid(b, p.operand(lo, 0))
	case HOAdd, HOSub, HOMul, HOXor:
		if n != 3 || !p.slotValid(b, p.operand(lo, 0)) {
			return false
		}
		t := p.slotType(b, p.operand(lo, 0))
		return p.matchesSlot(b, p.operand(lo, 1), t) && p.matchesSlot(b, p.operand(lo, 2), t)
	case HOLoad, HOStore, HOAddrOf, HOConvert:
		return n == 2 && p.slotValid(b, p.operand(lo, 0)) && p.slotValid(b, p.operand(lo, 1))
	case HOGetGlobal, HOSetGlobal:
		if n != 2 {
			return false
		}
		var sym, s uint32
		if k == HOGetGlobal {
			s, sym = p.operand(lo, 0), p.operand(lo, 1)
		} else {
			sym, s = p.operand(lo, 0), p.operand(lo, 1)
		}
		return p.symKind(sym, HSVar) && p.matchesSlot(b, s, p.word(HSymbols, int(sym)-1, 3))
	case HOCall, HOCallClosure, HOCallInstance:
		return p.callValid(b, k, lo, n)
	case HOJump:
		return n == 1 && p.operand(lo, 0) > 0 && p.operand(lo, 0) <= b.nops
	case HOBranch:
		return n == 5 &&
			p.operand(lo, 0) >= HCLess && p.operand(lo, 0) <= HCAtLeast &&
			p.slotValid(b, p.operand(lo, 1)) &&
			p.matchesSlot(b, p.operand(lo, 2), p.slotType(b, p.operand(lo, 1))) &&
			p.operand(lo, 3) > 0 && p.operand(lo, 3) <= b.nops &&
			p.operand(lo, 4) > 0 && p.operand(lo, 4) <= b.nops
	case HOReturn:
		nparam, nresult := p.signatureSize(b.sig)
		if n != nresult {
			return false
		}
		for j := uint32(0); j < nresult; j++ {
			if !p.matchesSlot(b, p.operand(lo, j), p.signatureType(b.sig, nparam+j)) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func (p *EIRPackage) validateFacts(b bodyCtx, first int) (int, error) {
	i := first
	var last, kind uint32
	for i < p.rows(HFacts) {
		x := p.word(HFacts, i, 0)
		k := p.word(HFacts, i, 1)
		if x > b.ops+b.nops {
			return i, nil
		}
		proof := p.word(HFacts, i, 4)
		if x <= b.ops || x < last || (x == last && k <= kind) || k != HFBounds ||
			!p.slotValid(b, p.word(HFacts, i, 2)) || !p.slotValid(b, p.word(HFacts, i, 3)) ||
			proof <= b.ops || proof >= x {
			return 0, fmt.Errorf("ergopkg: HFacts[%d] invalid identity, scope, or proof reference", i)
		}
		last = x
		kind = k
		i++
	}
	return i, nil
}

func (p *EIRPackage) validateBodies(start uint32) (uint32, error) {
	var sl, row uint32
	w := start
	bi := 0
	f := 0
	for si := 0; si < p.rows(HSymbols); si++ {
		if p.word(HSymbols, si, 0) != 0 || p.word(HSymbols, si, 2) != HSFunc {
			continue
		}
		if bi >= p.rows(HBodies) || p.word(HBodies, bi, 0) != uint32(si+1) {
			return 0, fmt.Errorf("ergopkg: HBodies[%d] missing or mismatched body for local func %d", bi, si+1)
		}
		sig := p.signature(p.word(HSymbols, si, 3))
		b := bodyCtx{
			sig:    sig,
			slots:  p.word(HBodies, bi, 1),
			nslots: p.word(HBodies, bi, 2),
			ops:    p.word(HBodies, bi, 3),
			nops:   p.word(HBodies, bi, 4),
		}
		if b.sig == 0 || b.nops == 0 || !p.run(HSlots, sl, b.slots, b.nslots) || !p.run(HOps, row, b.ops, b.nops) {
			return 0, fmt.Errorf("ergopkg: HBodies[%d] invalid signature or slot/op run", bi)
		}
		nparam, _ := p.signatureSize(sig)
		if nparam > b.nslots {
			return 0, fmt.Errorf("ergopkg: HBodies[%d] missing parameter slots", bi)
		}
		for j := uint32(0); j < b.nslots; j++ {
			t := p.slotType(b, j+1)
			if !p.ref(HTypes, t) || (j < nparam && t != p.signatureType(sig, j)) {
				return 0, fmt.Errorf("ergopkg: HSlots[%d] invalid slot type", sl+j)
			}
		}
		for j := int(row); j < int(row+b.nops); j++ {
			k := p.word(HOps, j, 0)
			sp := p.word(HOps, j, 1)
			lo := p.word(HOps, j, 2)
			n := p.word(HOps, j, 3)
			if !p.ref(HSpans, sp) || !p.run(HOperands, w, lo, n) || !p.opValid(b, k, lo, n) {
				return 0, fmt.Errorf("ergopkg: HOps[%d] invalid opcode, span, or operands", j)
			}
			w = lo + n
		}
		var err error
		f, err = p.validateFacts(b, f)
		if err != nil {
			return 0, err
		}
		sl += b.nslots
		row += b.nops
		bi++
	}
	if bi != p.rows(HBodies) || f != p.rows(HFacts) || sl != p.dir[HSlots].count || row != p.dir[HOps].count {
		return 0, fmt.Errorf("ergopkg: unowned body, slot, op, or fact")
	}
	return w, nil
}

func (p *EIRPackage) orderedArgs(a, an, b, bn uint32) bool {
	for j := uint32(0); j < an; j++ {
		if j >= bn {
			return false
		}
		left := p.operand(a, j)
		right := p.operand(b, j)
		if left != right {
			return left < right
		}
	}
	return an < bn
}

func (p *EIRPackage) dictionaryValid(d, s uint32) bool {
	return p.symKind(d, HSDictionary) && p.word(HSymbols, int(d)-1, 0) == 0 && p.word(HSymbols, int(d)-1, 3) == s
}

func (p *EIRPackage) validateInstances(start uint32) error {
	at := start
	var last, lastLo, lastN uint32
	for i := 0; i < p.rows(HInstances); i++ {
		g := p.word(HInstances, i, 0)
		s := p.word(HInstances, i, 1)
		d := p.word(HInstances, i, 2)
		lo := p.word(HInstances, i, 3)
		n := p.word(HInstances, i, 4)
		if !p.symKind(g, HSFunc) || !p.ref(HTypes, s) || p.typeKind(s) != HTSignature {
			return fmt.Errorf("ergopkg: HInstances[%d] invalid generic or specialization signature", i)
		}
		if !p.dictionaryValid(d, s) {
			return fmt.Errorf("ergopkg: HInstances[%d] missing or inconsistent dictionary", i)
		}
		t := p.genericType(g)
		if p.typeKind(t) != HTGeneric || n != p.word(HTypes, int(t)-1, 3) || !p.run(HOperands, at, lo, n) {
			return fmt.Errorf("ergopkg: HInstances[%d] invalid argument arity or operand run", i)
		}
		if g < last || (g == last && !p.orderedArgs(lastLo, lastN, lo, n)) {
			return fmt.Errorf("ergopkg: HInstances[%d] out of order", i)
		}
		for j := uint32(0); j < n; j++ {
			if !p.ref(HTypes, p.operand(lo, j)) {
				return fmt.Errorf("ergopkg: HInstances[%d] invalid argument type", i)
			}
		}
		at = lo + n
		last, lastLo, lastN = g, lo, n
	}
	if at != p.dir[HOperands].count {
		return fmt.Errorf("ergopkg: HOperands has unowned trailing words")
	}
	return nil
}

func (p *EIRPackage) validateInitOrder() error {
	seen := make([]bool, p.rows(HSymbols))
	for i := 0; i < p.rows(HInit); i++ {
		s := p.word(HInit, i, 0)
		j := int(s) - 1
		if !p.symKind(s, HSFunc) || seen[j] || p.word(HSymbols, j, 0) != 0 ||
			!strings.HasPrefix(p.nameOf(p.word(HSymbols, j, 1)), "init.") {
			return fmt.Errorf("ergopkg: HInit[%d] incomplete, duplicate, or foreign initializer", i)
		}
		sig := p.signature(p.word(HSymbols, j, 3))
		if sig == 0 || p.word(HTypes, int(sig)-1, 3) != 0 {
			return fmt.Errorf("ergopkg: HInit[%d] invalid initializer signature", i)
		}
		seen[j] = true
	}
	for i := 0; i < p.rows(HSymbols); i++ {
		if p.word(HSymbols, i, 0) == 0 && p.word(HSymbols, i, 2) == HSFunc &&
			strings.HasPrefix(p.nameOf(p.word(HSymbols, i, 1)), "init.") && !seen[i] {
			return fmt.Errorf("ergopkg: HSymbols[%d] initializer missing from HInit", i)
		}
	}
	return nil
}

func (p *EIRPackage) materialize() {
	p.Target = EIRTarget{
		Path:    p.nameOf(p.word(HPackage, 0, 0)),
		GOOS:    p.nameOf(p.word(HPackage, 0, 2)),
		GOARCH:  p.nameOf(p.word(HPackage, 0, 3)),
		Pin:     p.nameOf(p.word(HPackage, 0, 4)),
		Profile: p.nameOf(p.word(HPackage, 0, 5)),
	}
	p.PkgName = p.nameOf(p.word(HPackage, 0, 1))

	p.Imports = make([]EIRImport, p.rows(HImports))
	for i := range p.Imports {
		var fp [8]byte
		binary.LittleEndian.PutUint32(fp[0:4], p.word(HImports, i, 3))
		binary.LittleEndian.PutUint32(fp[4:8], p.word(HImports, i, 4))
		p.Imports[i] = EIRImport{
			Path:        p.nameOf(p.word(HImports, i, 0)),
			Name:        p.nameOf(p.word(HImports, i, 1)),
			HasInit:     p.word(HImports, i, 2) == 1,
			Fingerprint: fp,
		}
	}

	p.Files = make([]EIRFile, p.rows(HFiles))
	for i := range p.Files {
		f := EIRFile{
			Path:      p.nameOf(p.word(HFiles, i, 0)),
			ByteSize:  p.word(HFiles, i, 1),
			FirstLine: p.word(HFiles, i, 2),
			LineCount: p.word(HFiles, i, 3),
		}
		for r := 0; r < 8; r++ {
			f.Rev[r] = p.word(HFiles, i, 4+r)
		}
		p.Files[i] = f
	}

	p.Lines = make([]uint32, p.rows(HLines))
	for i := range p.Lines {
		p.Lines[i] = p.word(HLines, i, 0)
	}

	p.Spans = make([]EIRSpan, p.rows(HSpans))
	for i := range p.Spans {
		p.Spans[i] = EIRSpan{
			File:  p.word(HSpans, i, 0),
			Start: p.word(HSpans, i, 1),
			End:   p.word(HSpans, i, 2),
		}
	}

	p.Types = make([]EIRType, p.rows(HTypes))
	for i := range p.Types {
		p.Types[i] = EIRType{
			Kind:    p.word(HTypes, i, 0),
			Aux:     p.word(HTypes, i, 1),
			FirstOp: p.word(HTypes, i, 2),
			OpCount: p.word(HTypes, i, 3),
		}
	}

	p.Symbols = make([]EIRSymbol, p.rows(HSymbols))
	for i := range p.Symbols {
		p.Symbols[i] = EIRSymbol{
			Import: p.word(HSymbols, i, 0),
			Name:   p.nameOf(p.word(HSymbols, i, 1)),
			Kind:   p.word(HSymbols, i, 2),
			Type:   p.word(HSymbols, i, 3),
			Span:   p.word(HSymbols, i, 4),
		}
	}

	p.Bodies = make([]EIRBody, p.rows(HBodies))
	for i := range p.Bodies {
		p.Bodies[i] = EIRBody{
			FuncSym:   p.word(HBodies, i, 0),
			FirstSlot: p.word(HBodies, i, 1),
			SlotCount: p.word(HBodies, i, 2),
			FirstOp:   p.word(HBodies, i, 3),
			OpCount:   p.word(HBodies, i, 4),
		}
	}

	p.Slots = make([]uint32, p.rows(HSlots))
	for i := range p.Slots {
		p.Slots[i] = p.word(HSlots, i, 0)
	}

	p.Ops = make([]EIROp, p.rows(HOps))
	for i := range p.Ops {
		p.Ops[i] = EIROp{
			Opcode:  p.word(HOps, i, 0),
			Span:    p.word(HOps, i, 1),
			FirstOp: p.word(HOps, i, 2),
			OpCount: p.word(HOps, i, 3),
		}
	}

	p.Instances = make([]EIRInstance, p.rows(HInstances))
	for i := range p.Instances {
		p.Instances[i] = EIRInstance{
			GenericSym:     p.word(HInstances, i, 0),
			SpecializedSig: p.word(HInstances, i, 1),
			DictSym:        p.word(HInstances, i, 2),
			FirstOp:        p.word(HInstances, i, 3),
			OpCount:        p.word(HInstances, i, 4),
		}
	}

	p.Facts = make([]EIRFact, p.rows(HFacts))
	for i := range p.Facts {
		p.Facts[i] = EIRFact{
			Op:          p.word(HFacts, i, 0),
			Kind:        p.word(HFacts, i, 1),
			SubjectSlot: p.word(HFacts, i, 2),
			LimitSlot:   p.word(HFacts, i, 3),
			ProofOp:     p.word(HFacts, i, 4),
		}
	}

	p.Inits = make([]uint32, p.rows(HInit))
	for i := range p.Inits {
		p.Inits[i] = p.word(HInit, i, 0)
	}

	p.Operands = make([]uint32, p.rows(HOperands))
	for i := range p.Operands {
		p.Operands[i] = p.word(HOperands, i, 0)
	}
}

// AdmitEIR checks the target tuple and every peer import against observed Go archives.
func AdmitEIR(p *EIRPackage, wantTarget EIRTarget, observed []EIRImport) error {
	if p.Target != wantTarget {
		return fmt.Errorf("ergopkg: target mismatch: image has (%s %s/%s %s %s), compiler has (%s %s/%s %s %s)",
			p.Target.Path, p.Target.GOOS, p.Target.GOARCH, p.Target.Pin, p.Target.Profile,
			wantTarget.Path, wantTarget.GOOS, wantTarget.GOARCH, wantTarget.Pin, wantTarget.Profile)
	}
	if len(observed) != len(p.Imports) {
		return fmt.Errorf("ergopkg: import count mismatch: image has %d, observed %d", len(p.Imports), len(observed))
	}
	for i, want := range p.Imports {
		got := observed[i]
		if got.Path != want.Path || got.Name != want.Name || got.HasInit != want.HasInit || got.Fingerprint != want.Fingerprint {
			return fmt.Errorf("ergopkg: import %q mismatch (want name=%q hasInit=%v fp=%x; got name=%q hasInit=%v fp=%x)",
				want.Path, want.Name, want.HasInit, want.Fingerprint, got.Name, got.HasInit, got.Fingerprint)
		}
	}
	return nil
}
