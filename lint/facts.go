// Package lint is the public Go SDK for Ergo rule packs (docs/specs/custom-lint.md).
//
// A rule reads a versioned, read-only Snapshot of the checker's facts and
// reports findings. Facts carry resolved identities: a type, a callable or a
// context is named by the package that declares it and its declared name,
// never by the spelling at a use. A rule never reconstructs imports,
// shadowing, rows or provider choices from identifier text. The runner, not
// the rule, owns configuration, severity, ordering, limits and the
// projection to diagnostics.
//
// Every fact is plain data: records of strings, numbers, booleans and lists,
// and string tags for kinds. The same schema is expressible as Ergo records
// and unions, so a pack written in Ergo (LINT4) reads the same encoded form.
//
// This package imports nothing from the compiler: a pack depends on the fact
// schema, not on checker internals (TestSDKDoesNotImportCompilerInternals).
package lint

import "slices"

// FactSchemaName and FactSchemaVersion identify the snapshot's encoded contract.
// Version 2 is the schema of the Ergo checker (internal/sketch/check): it
// replaced version 1's services, providers, provisions and layers with
// operation types, contexts and withs. An additive optional field keeps the
// version; a change in the meaning or shape of a field increments it.
const (
	FactSchemaName    = "ergo.lint.facts"
	FactSchemaVersion = 2
)

// Family names one independently available group of facts. A rule declares
// the families it requires; a family absent from Snapshot.Families is
// unavailable and the rule is skipped, never passed.
type Family string

const (
	// FamilyDeclarations: the program's type declarations and named
	// contexts, with identities.
	FamilyDeclarations Family = "declarations"
	// FamilyCallables: functions, tests and laws with their written error
	// and capability rows and their derived footprints.
	FamilyCallables Family = "callables"
	// FamilyCalls: every checked call inside a callable, with its resolved
	// callee and the error row at the site.
	FamilyCalls Family = "calls"
	// FamilyWiths: every with, with the provider each capability it
	// discharges is bound to and what it acquires.
	FamilyWiths Family = "withs"
	// FamilyBindings: local bindings and their resolved uses.
	FamilyBindings Family = "bindings"
	// FamilyImports: each file's imports and the package each resolved to.
	FamilyImports Family = "imports"
)

// Families lists every family of FactSchemaVersion in canonical order.
func Families() []Family {
	return []Family{FamilyDeclarations, FamilyCallables, FamilyCalls, FamilyWiths, FamilyBindings, FamilyImports}
}

func knownFamily(family Family) bool { return slices.Contains(Families(), family) }

// Unavailable reasons. A rule requiring an unavailable family is skipped
// with the family's reason.
const (
	ReasonUncheckedSource = "unchecked-source"
	ReasonNotRequested    = "not-requested"
)

// Span is a place in one source file: the file (Source.File), a UTF-8
// byte offset and length, and the 1-based line and byte column of its
// start, as the compiler counts them.
type Span struct {
	File   string `json:"file"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}

// Schema identifies the fact schema of a snapshot.
type Schema struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
}

// Semantic qualifies the compiler's analysis. Revision is the digest of
// the analysed sources; Producer and ReuseScope identify the analysis
// producer. Lint options and packs are not part of it: they qualify an
// AnalysisIdentity, not the program.
type Semantic struct {
	Revision   string `json:"revision"`
	Target     string `json:"target"`
	Producer   string `json:"producer,omitempty"`
	ReuseScope string `json:"reuseScope,omitempty"`
}

// Source is one analysed file. File is its path relative to the module
// root ("" for a program of one source text); every Span names one. Bytes
// bounds every span in it. Text is the file itself, which a host supplies so
// that the runner can check finding positions against it; it is never sent
// to a pack. When set, it has exactly Bytes bytes.
type Source struct {
	File  string `json:"file"`
	URI   string `json:"uri,omitempty"`
	Bytes int    `json:"bytes"`
	Text  string `json:"-"`
}

// UnavailableFamily explains why a family was not produced.
type UnavailableFamily struct {
	Family Family `json:"family"`
	Reason string `json:"reason"`
}

// Snapshot is one read-only fact projection of a checked program. Every
// list is in a deterministic order: equal sources checked by equal
// producers give byte-identical JSON.
type Snapshot struct {
	Schema      Schema              `json:"schema"`
	Semantic    Semantic            `json:"semantic"`
	Sources     []Source            `json:"sources"`
	Checked     bool                `json:"checked"`
	Families    []Family            `json:"families"`
	Unavailable []UnavailableFamily `json:"unavailable,omitempty"`

	Types     []TypeDecl `json:"types,omitempty"`
	Contexts  []Context  `json:"contexts,omitempty"`
	Callables []Callable `json:"callables,omitempty"`
	Calls     []Call     `json:"calls,omitempty"`
	Withs     []With     `json:"withs,omitempty"`
	Bindings  []Binding  `json:"bindings,omitempty"`
	Imports   []Import   `json:"imports,omitempty"`
}

// Type declaration kinds.
const (
	TypeRecord    = "record"
	TypeUnion     = "union"
	TypeOperation = "operation"
	TypeDefined   = "defined"
)

// TypeDecl is a type the program declares. Identity is "type:" and the
// declaring package's path, a dot and the name; a type of std or of a host
// binding that a row or a with names has the same form and no declaration
// here. Variants are a union's variant identities; Operations an operation
// type's operation names.
type TypeDecl struct {
	Identity   string   `json:"identity"`
	Package    string   `json:"package"`
	Name       string   `json:"name"`
	Kind       string   `json:"kind"`
	Exported   bool     `json:"exported,omitempty"`
	Span       Span     `json:"span"`
	Variants   []string `json:"variants,omitempty"`
	Operations []string `json:"operations,omitempty"`
}

// Context is a named context the program declares: what each entry binds.
type Context struct {
	Identity string         `json:"identity"`
	Package  string         `json:"package"`
	Name     string         `json:"name"`
	Span     Span           `json:"span"`
	Entries  []ContextEntry `json:"entries,omitempty"`
}

// ContextEntry binds Capability (a type identity) to a provider whose type
// is Provider. Converts is false when the provider's type is the capability
// itself: a capability value passed on, which picks no implementation.
// Span is the provider expression.
type ContextEntry struct {
	Capability string `json:"capability"`
	Provider   string `json:"provider"`
	Converts   bool   `json:"converts,omitempty"`
	Span       *Span  `json:"span,omitempty"`
}

// Callable kinds.
const (
	CallableFunction = "function"
	CallableTest     = "test"
	CallableLaw      = "law"
)

// Callable is a function, test or law of the program. Identity is
// "func:", "test:" or "law:" and the package path, a dot and the name, with
// "#N" after the N-th later declaration of the same name in the package
// (functions reached by x.f(…) may share a name). Fails is the error row
// and Needs the capability row, as checked: every error that can escape
// the callable is in Fails, so a failure a helper raises is there when the
// callable passes it on and absent when the callable recovers it.
// Footprint is derived from the body (rows.md, Footprints).
type Callable struct {
	Identity  string    `json:"identity"`
	Package   string    `json:"package"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	Exported  bool      `json:"exported,omitempty"`
	Span      Span      `json:"span"`
	Extent    Span      `json:"extent"`
	Fails     []string  `json:"fails,omitempty"`
	Needs     []string  `json:"needs,omitempty"`
	Footprint Footprint `json:"footprint"`
}

// Footprint is what a callable's body does when it runs: the capabilities
// it exercises (type identities), whether it starts a task, and whether it
// runs code whose footprint is unknown. An empty footprint is a pure
// callable.
type Footprint struct {
	Capabilities []string `json:"capabilities,omitempty"`
	Tasks        bool     `json:"tasks,omitempty"`
	Opaque       bool     `json:"opaque,omitempty"`
}

// Call kinds.
const (
	CallFunction   = "function"
	CallOperation  = "operation"
	CallCapability = "capability"
	CallValue      = "value"
	CallConversion = "conversion"
	CallWith       = "with"
	CallBuiltin    = "builtin"
)

// Call is one checked call inside Caller. Callee is the resolved function
// identity of a function call, or the operation's type identity and name
// ("type:pkg.T.Op") of an operation or capability call; a call of a
// function value has none. Fails is the error row at the site.
type Call struct {
	Caller string   `json:"caller"`
	Callee string   `json:"callee,omitempty"`
	Kind   string   `json:"kind"`
	Span   Span     `json:"span"`
	Fails  []string `json:"fails,omitempty"`
}

// With is one with inside Callable. Contexts are the identities of the
// named contexts it binds (none for an inline or a local context). Entries
// are the capabilities its block needs that it discharges, each with the
// provider it is bound to, whichever context spelled the entry. Acquired
// are the provider types it builds and owns, in acquisition order.
type With struct {
	Callable string         `json:"callable"`
	Span     Span           `json:"span"`
	Contexts []string       `json:"contexts,omitempty"`
	Entries  []ContextEntry `json:"entries,omitempty"`
	Acquired []string       `json:"acquired,omitempty"`
}

// Binding is one local binding inside Callable: a parameter, a let, a
// pattern, a case or a loop binding. Type is its type as the checker
// prints it. Uses are the places the checker resolved to it.
type Binding struct {
	Identity string `json:"identity"`
	Callable string `json:"callable"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Mutable  bool   `json:"mutable,omitempty"`
	Span     Span   `json:"span"`
	Uses     []Span `json:"uses,omitempty"`
}

// Import kinds: the package an import resolved to.
const (
	ImportStd     = "std"
	ImportProgram = "program"
	ImportHost    = "host"
)

// Import is one import of a file: the name the file binds, the path of the
// package it resolved to and that package's kind.
type Import struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Kind string `json:"kind"`
	Span Span   `json:"span"`
}

// Has reports whether family was produced.
func (s *Snapshot) Has(family Family) bool { return slices.Contains(s.Families, family) }

// UnavailableReason reports why family is unavailable, or "" when present.
func (s *Snapshot) UnavailableReason(family Family) string {
	if s.Has(family) {
		return ""
	}
	for _, unavailable := range s.Unavailable {
		if unavailable.Family == family {
			return unavailable.Reason
		}
	}
	return ReasonNotRequested
}

// Callable returns the callable with identity, or nil.
func (s *Snapshot) Callable(identity string) *Callable {
	for i := range s.Callables {
		if s.Callables[i].Identity == identity {
			return &s.Callables[i]
		}
	}
	return nil
}

// Declaration lookups select what a user configured: "PKG.Name", the path
// of the declaring package, a dot and the declared name. They read
// declarations only, never a use-site spelling: rules then compare the
// identities they return.

// TypeIdentity is the identity of the type "PKG.Name" names; it need not be
// declared in the snapshot (a std or host type).
func TypeIdentity(qualified string) string { return "type:" + qualified }

// FunctionNamed returns the functions declared as "PKG.Name": several when
// x.f(…) functions share the name.
func (s *Snapshot) FunctionNamed(qualified string) []*Callable {
	var out []*Callable
	for i := range s.Callables {
		c := &s.Callables[i]
		if c.Kind == CallableFunction && c.Package+"."+c.Name == qualified {
			out = append(out, c)
		}
	}
	return out
}

// TypeNamed returns the type the program declares as "PKG.Name", or nil.
func (s *Snapshot) TypeNamed(qualified string) *TypeDecl {
	for i := range s.Types {
		if s.Types[i].Identity == TypeIdentity(qualified) {
			return &s.Types[i]
		}
	}
	return nil
}
