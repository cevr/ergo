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
// each site lowers to its checking form. For a bounds fact the checking
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
	// referenced reports whether the build selects the reference lowering.
	referenced func() bool
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
		name:       "own-read",
		class:      valueClass,
		admission:  "keptRead",
		reference:  "valueLowering=copy",
		agreement:  "TestHandoverAgrees",
		mutant:     "own-read-element-write-blind",
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
// test build, which lowers every site to its checking form, and not when
// the shortcut's reference lowering is selected. Only the shortcut's
// admission function asks (TestShortcutsAreRegistered).
func (l *lowerer) takes(name string) bool {
	s := shortcutNamed(name)
	if l.tests != nil {
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
