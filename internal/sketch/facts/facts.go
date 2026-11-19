// Package facts holds the fact tables of a checked package (SEMTOOLS
// SEM0): its symbols, the sites where each symbol is named, the same
// sites by position, the relations between symbols, and its machines.
// Queries, rename, codemods and lint rules read these tables; no tool
// walks the typed trees to answer a name question.
//
// The checker is the one producer of the facts. Until the Ergo checker
// keeps them itself (capstone S3), Build writes them from the checker's
// result (check.Info) in one walk of the package's files. Scan is the
// reference: a plain scan of the checker's maps, slow and obviously right,
// which the tables must equal (TestTablesAgreeWithScan).
//
// A row that names a symbol of another package holds that symbol's handle,
// never a pointer: a handle has the same meaning in every process and at
// every revision while the declaration keeps its qualified name (Go's
// objectpath: "a pointer has meaning only within one address space").
// Inside one table a symbol is a dense id: its index in Symbols.
//
// Prior art: Gleam's compiler records kinded references while it
// type-checks, and rename and the unused warnings read them; gopls writes
// a per-package cross-reference index during type checking, keyed by
// objectpath.
package facts

// Kind is a symbol's kind. It is a closed set: a tool that switches on it
// covers every kind or names its default.
type Kind uint8

// The symbol kinds.
const (
	Package   Kind = iota + 1 // an imported package (its handle is its path)
	Func                      // a package function, extern included
	Type                      // a record, operation type, union or defined type; a predeclared type
	Variant                   // a variant of a union
	Field                     // a field of a record or a variant
	Operation                 // an operation of an operation type
	Machine                   // a machine declaration
	Law                       // a law: top level, member of an operation type, or of a machine
	Test                      // a test block
	Context                   // a named context
	Let                       // a package-level let
	Param                     // a parameter of a function, law or test
	TypeParam                 // a type parameter
	Local                     // a local binding: let, pattern, loop, catch binding, local context
	kindEnd
)

var kindNames = [...]string{"", "package", "func", "type", "variant", "field", "operation", "machine", "law", "test", "context", "let", "param", "typeparam", "local"}

func (k Kind) String() string {
	if k < kindEnd {
		return kindNames[k]
	}
	return "kind?"
}

// SiteKind is the way a site names its symbol. Each site has one kind.
type SiteKind uint8

// The site kinds.
const (
	Def       SiteKind = iota + 1 // the declaring name
	Read                          // a value read, a package qualifier, a capability named in a body
	Write                         // the target of an assignment: its root and, for a field, the field
	Call                          // the callee of a call (a function, an operation, a dot call)
	TypeUse                       // a type named in a type position
	Construct                     // the type of a value built (a literal or a variant), and a field key set in it
	Match                         // a type or field key in a pattern of match, let or for
	Import                        // an import of the package
	Needs                         // a capability in a needs row
	Fails                         // a failure in a fails row
	Provides                      // a capability a context provides
	Binds                         // a capability a context entry binds
	Handles                       // a failure a catch arm handles
	siteKindEnd
)

var siteKindNames = [...]string{"", "def", "read", "write", "call", "typeuse", "construct", "match", "import", "needs", "fails", "provides", "binds", "handles"}

func (k SiteKind) String() string {
	if k < siteKindEnd {
		return siteKindNames[k]
	}
	return "site?"
}

// Inst marks a site that instantiates a generic function: Info.Instances
// has the call or the index expression the site names.
const Inst uint8 = 1

// NoSym is the Encl of a site outside every declaration (an import).
const NoSym = ^uint32(0)

// Symbol is one symbol a table names: declared in its package (Declared),
// or declared elsewhere and named here.
type Symbol struct {
	// Handle is the printable, stable name: path.Name, path.Type.Member,
	// path.Func.param. Empty for a local the declaration binds more than
	// once, and for a local context: those are addressed by position.
	Handle string
	Kind   Kind
	// Name is the declared name.
	Name string
	// Declared reports a symbol of this table's package; File and Off are
	// then its declaring name's position.
	Declared bool
	File     uint16
	Off      uint32
	Exported bool
}

// Site is one place a symbol is named: 16 bytes.
type Site struct {
	Sym  uint32 // the symbol's id in the table
	Off  uint32 // the byte offset of the name in its file
	Encl uint32 // the enclosing declaration's symbol (a top-level one, or a member law), or NoSym
	File uint16
	Kind SiteKind
	Flag uint8
}

// Edge is one relation between two symbols of a table, with the site that
// makes it, or NoSym.
type Edge struct {
	From, To uint32
	Site     uint32
}

// Table is the fact tables of one package.
type Table struct {
	// Path is the package's import path.
	Path string
	// Files are the package's files, by the name a diagnostic gives them.
	Files []string
	// Symbols are in the order the walk met them, so equal programs give
	// equal tables.
	Symbols []Symbol
	// Sites are sorted by symbol, then file, then offset; the sites of
	// symbol s are Sites[Start[s]:Start[s+1]].
	Sites []Site
	Start []uint32
	// ByPos is the sites by file and offset, as indexes into Sites.
	ByPos []uint32
	// Handles is the ids of the symbols with a handle, sorted by handle.
	Handles []uint32
	// Variants are union → variant; Calls are caller → callee, one per
	// call site (Site is its index in Sites); Ops are operation →
	// function, an operation type's operation implemented by a package
	// function at a conversion (Site is NoSym).
	Variants, Calls, Ops []Edge
	// Needs are function → capability, each operation type of the
	// function's needs row as the checker derived it (Site is NoSym).
	// Brings and Discharges are caller → capability, one per call site
	// and capability the call charges (Site is the call's site, as in
	// Calls): Discharges when a with around the call discharges it,
	// Brings otherwise (rows.go). Provided are provider → capability, one
	// per discharge and provider of the innermost with that discharges it
	// (Site is the call's site).
	Needs, Brings, Discharges, Provided []Edge
	// Fails are function → failure, each element of the function's error
	// row as the checker derived it (Site is NoSym). Raises and Caught are
	// caller → failure, one per call site and failure the call raises:
	// Caught when a catch around the call handles it, Raises otherwise.
	Fails, Raises, Caught []Edge
	// Machines are the package's machine declarations, in source order.
	Machines []MachineGraph
}

// MachineGraph is one machine's graph (internal/sketch/machinegraph),
// with its maps in order: Timers, Work and Exit (the states' exit
// actions, FK9-g1) in the states' order, each Work and Exit place naming
// its state in What.
type MachineGraph struct {
	Sym     uint32
	States  []string
	Signals []string
	Initial string
	Arms    []Arm
	Timers  []Timer
	Work    []Place
	Exit    []Place
	Unsure  []string
}

// Arm is one arm of a machine's step. Alts holds each pattern of a case
// of several tuple patterns (machinegraph.Arm.Alts): the arm takes a pair
// one of them takes, not every pair of its States and Signals.
type Arm struct {
	At      Place
	States  []string
	Signals []string
	Partial bool
	Edges   []Transition
	Alts    []Alt
}

// Alt is one pattern of an arm: the state and signal variants it takes
// (nil for any), and whether it tests more than the variants.
type Alt struct {
	States  []string
	Signals []string
	Partial bool
}

// Transition is one decision an arm can give.
type Transition struct {
	Decision string
	To       string
	At       Place
}

// Timer is the timer of a state's entry.
type Timer struct {
	State, Signal string
	At            Place
}

// Place is a line of a file of the table: a state's work names its state
// in What.
type Place struct {
	What string
	File string
	Line int
}

// Refs is the sites of symbol id.
func (t *Table) Refs(id uint32) []Site { return t.Sites[t.Start[id]:t.Start[id+1]] }

// Lookup is the id of the symbol with handle h, by binary search; probes
// counts the comparisons.
func (t *Table) Lookup(h string) (id uint32, probes int, ok bool) {
	lo, hi := 0, len(t.Handles)
	for lo < hi {
		probes++
		m := int(uint(lo+hi) >> 1)
		if t.Symbols[t.Handles[m]].Handle < h {
			lo = m + 1
		} else {
			hi = m
		}
	}
	if lo < len(t.Handles) && t.Symbols[t.Handles[lo]].Handle == h {
		return t.Handles[lo], probes, true
	}
	return 0, probes, false
}

// At is the site whose name covers offset off of file, by binary search.
func (t *Table) At(file uint16, off uint32) (Site, bool) {
	lo, hi := 0, len(t.ByPos)
	for lo < hi {
		m := int(uint(lo+hi) >> 1)
		s := t.Sites[t.ByPos[m]]
		if s.File < file || s.File == file && s.Off <= off {
			lo = m + 1
		} else {
			hi = m
		}
	}
	if lo == 0 {
		return Site{}, false
	}
	s := t.Sites[t.ByPos[lo-1]]
	if s.File != file || off >= s.Off+uint32(len(t.Symbols[s.Sym].Name)) {
		return Site{}, false
	}
	return s, true
}
