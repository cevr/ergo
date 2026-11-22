package lower

// Shortcuts (ASSERT1; plan IR-G §2, items 1 to 4). A shortcut is a
// lowering that takes a faster form at a site because a proof says the
// site allows it: an unchecked load where a bounds fact proves the index
// in range (BCE1), an in-place write where liveness proves the old value
// dead (OWN1). A wrong proof gives a wrong value (value class), corrupts
// memory (memory class) or hides a report (detector class).
//
// One registry, shortcuts below, lists every shortcut with its name, its
// trust class, its one admission function (the function that decides a
// site and the only one that asks takes for the shortcut), its reference
// lowering (the test hook that selects the old form), its agreement test
// (the seeded property that holds the new form to the reference or to a
// model) and its ledger mutant (an edit that takes the shortcut without
// the proof's condition, which the agreement test kills). A memory-class
// shortcut also names the runtime bodies it calls, each listed in
// internal/residue/testdata/unsafe.txt.
//
// A test build (`ergo test`) takes no shortcut: takes is false there, so
// each site lowers to its checking form. The exception is a row marked
// inTests, whose form has no checking form to fall back to (MONO1's
// copies): there the test build runs what ships. For a bounds fact the checking
// form is Go's checked index; for a handover it is the copy, with the old
// storage poisoned (handover.go), so a read through an alias the proof
// missed reads poison and fails the program's own tests. The receipt of
// a shortcut is its site count: Taken in the lowering's stats.
//
// internal/residue's TestShortcutsAreRegistered holds the registry to the
// source: every takes call names a row and sits in that row's admission
// function, and every row has its admission, reference, agreement test
// and mutant. TestTestBuildsTakeNoShortcut lowers a witness of each row
// as a run build (the shortcut is taken) and as a test build (it is not).
//
// Prior art: Ada SPARK's Assertion_Policy turns proof obligations into
// run-time checks in a checking build; Zig's safe and fast build modes;
// Swift checks exclusivity statically where it proves it and dynamically
// in debug builds; Rust's debug_assert! and its overflow checks exist in
// debug builds only. Gleam, ReScript and Elixir have no proof-selected
// lowering: they take the host's checks (BEAM, JavaScript) as given.

// trust is a shortcut's trust class (plan IR-G §2 item 2).
type trust string

const (
	valueClass    trust = "value"    // a wrong decision gives a wrong value
	memoryClass   trust = "memory"   // a wrong decision corrupts memory
	detectorClass trust = "detector" // a wrong decision hides a report
	// checkedClass: the shortcut checks its condition at run time at each
	// site and takes the plain form when it fails; a wrong static premise
	// of the check gives a wrong value (own-lineage's token).
	checkedClass trust = "checked"
)

// shortcut is one row of the registry.
type shortcut struct {
	name      string
	class     trust
	admission string // the function that decides a site
	reference string // the test hook and value that select the reference lowering
	agreement string // the property test that holds it to the reference or a model
	mutant    string // the ledger mutant that drops the proof's condition
	unsafe    []string
	// consumer is, for a fact kind (facts.go), the Go pass that reads the
	// exported fact: a memory-class row names its runtime bodies or its
	// consumer.
	consumer string
	// referenced reports whether the build selects the reference lowering.
	referenced func() bool
	// inTests marks a shortcut the test build takes too: its form rests on
	// no proof a checking form could catch failing (an exact rewrite its
	// agreement test holds equal to the reference), so a test build runs
	// what ships (MONO-HOOKS-MIN, 2026-11-19).
	inTests bool
}

// shortcuts is the registry.
var shortcuts = []shortcut{
	{
		name:       "bounds-unchecked",
		class:      memoryClass,
		admission:  "uncheckedOK",
		reference:  "factLowering=checked",
		agreement:  "TestBoundsFactsSound",
		mutant:     "bounds-wrap-taken",
		unsafe:     []string{"runtime/v1/unchecked.go ByteAt", "runtime/v1/unchecked.go StringAt", "runtime/v1/unchecked.go StringN", "runtime/v1/unchecked.go AddrAt"},
		referenced: func() bool { return factLowering == "checked" },
	},
	// The fact kinds (facts.go, FACT0): each leaves the run build for a Go
	// pass that waits for the IR batch; the export changes no Go text.
	{
		name:       "fact-range",
		class:      memoryClass,
		admission:  "admitFact",
		reference:  "factExport=off",
		agreement:  "TestAssumptionsAgree",
		mutant:     "fact-on-wrong-value",
		consumer:   "FACT1, ARITH1: Go's prove pass (ssa/ergofacts.go)",
		referenced: func() bool { return factExport == "off" },
	},
	{
		name:       "fact-index",
		class:      memoryClass,
		admission:  "admitFact",
		reference:  "factExport=off",
		agreement:  "TestAssumptionsAgree",
		mutant:     "fact-wrong-dominator",
		consumer:   "FACT1, IRB1: Go's prove pass (ssa/ergofacts.go)",
		referenced: func() bool { return factExport == "off" },
	},
	{
		name:       "fact-cut",
		class:      memoryClass,
		admission:  "admitFact",
		reference:  "factExport=off",
		agreement:  "TestAssumptionsAgree",
		mutant:     "fact-cut-unproved",
		consumer:   "FACT1, IRB1: Go's prove pass (ssa/ergofacts.go)",
		referenced: func() bool { return factExport == "off" },
	},
	{
		name:       "fact-disjoint",
		class:      memoryClass,
		admission:  "admitFact",
		reference:  "factExport=off",
		agreement:  "TestAssumptionsAgree",
		mutant:     "fact-disjoint-unchecked",
		consumer:   "ALIAS1: Go's disjoint (ssa/ergofacts.go)",
		referenced: func() bool { return factExport == "off" },
	},
	{
		name:       "fact-pure",
		class:      memoryClass,
		admission:  "admitFact",
		reference:  "factExport=off",
		agreement:  "TestAssumptionsAgree",
		mutant:     "fact-pure-with-borrow",
		consumer:   "PURE1: Go's CSE, dead code and loop-invariant passes (ssa/ergofacts.go)",
		referenced: func() bool { return factExport == "off" },
	},
	{
		name:       "own-consume",
		class:      valueClass,
		admission:  "ownedFormAt",
		reference:  "valueLowering=copy",
		agreement:  "TestHandoverAgrees",
		mutant:     "own-concat-live-first-operand",
		referenced: func() bool { return !valueOwned() },
	},
	{
		name:       "own-field",
		class:      valueClass,
		admission:  "ownFieldOK",
		reference:  "valueLowering=copy",
		agreement:  "TestHandoverAgrees",
		mutant:     "own-update-field-of-live-record",
		referenced: func() bool { return !valueOwned() },
	},
	{
		name:       "own-lineage",
		class:      checkedClass,
		admission:  "lineageOK",
		reference:  "valueLowering=copy",
		agreement:  "TestHandoverAgrees",
		mutant:     "own-lineage-result-fresh",
		referenced: func() bool { return !valueOwned() },
	},
	{
		// LF1: a live slice goes to a Go function whose binding says it
		// only reads the operand with no copy (gofacts.go). The test build
		// copies it and compares after the call (v1.GoReadsCheck).
		name:       "host-reads",
		class:      valueClass,
		admission:  "readsOK",
		reference:  "hostCopyAlways=true",
		agreement:  "TestHostCopyElisionAgrees",
		mutant:     "ffi-reads-without-fact",
		referenced: func() bool { return hostCopyAlways },
	},
	{
		// LF2b (owner q193): a Go call that only reads a string's bytes
		// gets a view of them, not a copy (view.go). The test build passes
		// a guarded copy that faults at a write, a read past the end and a
		// late read.
		name:       "host-view",
		class:      memoryClass,
		admission:  "viewOK",
		reference:  "hostViewLowering=copy",
		agreement:  "TestHostViewAgrees",
		mutant:     "view-on-assumed",
		unsafe:     []string{"runtime/v1/view.go BytesView"},
		referenced: func() bool { return hostViewLowering == "copy" },
	},
	{
		// MONO1: a call of a generic function whose type arguments are
		// known Go types goes to a plain copy for them (mono.go). The
		// proof is the checker's instantiation. The test build takes it
		// too (MONO-HOOKS-MIN): tests run what ships, and a copy is
		// compiled once, in its generic's package, where a stencil is
		// compiled again in every package and shard that instantiates it.
		// TestGenericLoweringsAgree holds the copies to the stenciled
		// reference.
		name:       "mono-instance",
		class:      valueClass,
		admission:  "monoTaken",
		reference:  "genericLowering=stencil",
		agreement:  "TestGenericLoweringsAgree",
		mutant:     "mono-shares-unequal-bodies",
		referenced: func() bool { return genericLowering == "stencil" },
		inTests:    true,
	},
	{
		// NOSCAN1: a record's Go struct lists its pointer-holding fields
		// first (layout.go). No value fact: the order is a function of the
		// declaration; the admission is that no Go code outside the
		// program sees the struct. The test build keeps the declared order.
		name:       "layout-pointers-first",
		class:      valueClass,
		admission:  "layoutOrder",
		reference:  "layoutLowering=declared",
		agreement:  "TestLayoutsAgree",
		mutant:     "layout-codec-reads-reordered-field",
		referenced: func() bool { return layoutLowering == "declared" },
	},
	{
		name:       "own-read",
		class:      valueClass,
		admission:  "keptRead",
		reference:  "valueLowering=copy",
		agreement:  "TestHandoverAgrees",
		mutant:     "own-read-element-write-blind",
		referenced: func() bool { return !valueOwned() },
	},
	{
		name:       "own-call-pass",
		class:      valueClass,
		admission:  "passedApart",
		reference:  "borrowPassLowering=off",
		agreement:  "TestHandoverAgrees",
		mutant:     "own-call-pass-write-blind",
		referenced: func() bool { return !valueOwned() || !borrowPassOn() },
	},
	{
		name:       "own-enter-field",
		class:      valueClass,
		admission:  "sharedField",
		reference:  "valueLowering=copy",
		agreement:  "TestHandoverAgrees",
		mutant:     "own-enter-field-write-blind",
		referenced: func() bool { return !valueOwned() },
	},
	{
		name:       "own-borrow-return",
		class:      valueClass,
		admission:  "borrowRetSites",
		reference:  "valueLowering=copy",
		agreement:  "TestHandoverAgrees",
		mutant:     "own-borrow-return-caller-blind",
		referenced: func() bool { return !valueOwned() },
	},
	{
		name:       "own-reslice",
		class:      valueClass,
		admission:  "reslices",
		reference:  "valueLowering=copy",
		agreement:  "TestHandoverAgrees",
		mutant:     "own-reslice-other-place",
		referenced: func() bool { return !valueOwned() },
	},
	{
		name:       "own-fresh-call",
		class:      valueClass,
		admission:  "freshCall",
		reference:  "valueLowering=copy",
		agreement:  "TestHandoverAgrees",
		mutant:     "own-fresh-call-shared-operand",
		referenced: func() bool { return !valueOwned() },
	},
	{
		name:       "own-return",
		class:      valueClass,
		admission:  "returnsBefore",
		reference:  "valueLowering=copy",
		agreement:  "TestHandoverAgrees",
		mutant:     "reuse-return-whole-on-consuming-route",
		referenced: func() bool { return !valueOwned() },
	},
	{
		name:       "own-parts",
		class:      valueClass,
		admission:  "partsTaken",
		reference:  "valueLowering=copy",
		agreement:  "TestHandoverAgrees",
		mutant:     "reuse-parts-overlap",
		referenced: func() bool { return !valueOwned() },
	},
	{
		name:       "own-move",
		class:      valueClass,
		admission:  "baseCovers",
		reference:  "valueLowering=copy",
		agreement:  "TestHandoverAgrees",
		mutant:     "reuse-move-shared-field",
		referenced: func() bool { return !valueOwned() },
	},
	{
		name:       "own-catch",
		class:      valueClass,
		admission:  "catchTaken",
		reference:  "valueLowering=copy",
		agreement:  "TestHandoverAgrees",
		mutant:     "reuse-catch-arm-shares",
		referenced: func() bool { return !valueOwned() },
	},
	{
		name:       "own-update-self",
		class:      valueClass,
		admission:  "updateSelf",
		reference:  "valueLowering=copy",
		agreement:  "TestHandoverAgrees",
		mutant:     "reuse-update-self-reads-old",
		referenced: func() bool { return !valueOwned() },
	},
	{
		name:       "own-ref-in-place",
		class:      valueClass,
		admission:  "inPlaceOf",
		reference:  "valueLowering=copy",
		agreement:  "TestValueRuleAgrees",
		mutant:     "own-ref-in-place-cloned-init",
		referenced: func() bool { return !valueOwned() },
	},
	{
		name:       "uni-slot",
		class:      valueClass,
		admission:  "slotAdmit",
		reference:  "slotLowering=off",
		agreement:  "TestFlatUnionsAgree",
		mutant:     "uni-slot-shared-by-two-live-results",
		referenced: func() bool { return slotLowering == "off" },
	},
}

// shortcutNamed is the registry's row named name.
func shortcutNamed(name string) *shortcut {
	for i := range shortcuts {
		if shortcuts[i].name == name {
			return &shortcuts[i]
		}
	}
	panic("lower: the shortcut " + name + " is not in the registry (shortcuts.go)")
}

// takes reports whether this build may take the shortcut name: not in a
// test build, which lowers every site to its checking form, unless the
// shortcut is one the test build takes too (inTests), and not when the
// shortcut's reference lowering is selected. Only the shortcut's
// admission function asks (TestShortcutsAreRegistered).
func (l *lowerer) takes(name string) bool {
	s := shortcutNamed(name)
	if l.tests != nil && !s.inTests {
		return false
	}
	return !s.referenced()
}

// took counts a site that took the shortcut name (the receipt, Taken).
func (l *lowerer) took(name string) {
	if l.stats.Taken == nil {
		l.stats.Taken = map[string]int{}
	}
	l.stats.Taken[name]++
}
