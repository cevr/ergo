package check

// Code is a registered diagnostic code of the language's S namespace
// (docs/spec/diagnostics.md). A test keeps this table, the codes the parser
// and the checker emit, and the rows of diagnostics.md in step (#79's
// mechanism for the S namespace; #79 later joins it with the EF registry).
//
// Prior art: rustc_error_codes keeps one registered table that a tidy check
// compares with the emitted codes; TypeScript's diagnosticMessages.json.
type Code struct {
	Code   string
	Name   string
	Status string // "implemented" or "designed"
	Anchor string // the diagnostics.md section
}

// The codes the checker emits.
const (
	CodeSyntax       = "S001"
	CodeUndeclared   = "S101" // an error escapes and is not in the row
	CodeMissingCap   = "S102" // a charged capability is not in the row and no with discharges it
	CodeNotTotal     = "S103" // a catch or type switch omits a case
	CodeUnknown      = "S104" // a name does not resolve, or resolves to the wrong role
	CodeUnhandled    = "S105" // a fallible expression in a value position
	CodeProvider     = "S106" // a context provider does not implement its capability
	CodeMissingRet   = "S107" // a function with a result can reach its end
	CodeType         = "S108" // a type or declaration misuse
	CodeOutside      = "S109" // a construct owned by another ticket
	CodeUnused       = "S110" // an unread local, binding or import, or a value statement
	CodeDuplicateCap = "S111" // two entries for one capability in a context
	CodeEntry        = "S113" // the entry function and test functions
	CodeAuthority    = "S114" // a record built outside the package that has its construction authority
)

// Codes is the S code table: every code the parser or the checker emits.
var Codes = []Code{
	{CodeSyntax, "syntax", "implemented", "s001-syntax"},
	{CodeUndeclared, "undeclared error", "implemented", "s101-undeclared-error"},
	{CodeMissingCap, "missing capability", "implemented", "s102-missing-capability"},
	{CodeNotTotal, "not total", "implemented", "s103-not-total"},
	{CodeUnknown, "unknown name", "implemented", "s104-unknown-name"},
	{CodeUnhandled, "unhandled failure", "implemented", "s105-unhandled-failure"},
	{CodeProvider, "bad provider", "implemented", "s106-bad-provider"},
	{CodeMissingRet, "missing return", "implemented", "s107-missing-return"},
	{CodeType, "type or declaration misuse", "implemented", "s108-type-or-declaration-misuse"},
	{CodeOutside, "owned by another ticket", "implemented", "s109-owned-by-another-ticket"},
	{CodeUnused, "unused", "implemented", "s110-unused"},
	{CodeDuplicateCap, "duplicate context entry", "implemented", "s111-duplicate-context-entry"},
	{"S112", "resource map key", "designed", "s112-resource-map-key"},
	{CodeEntry, "entry and test functions", "implemented", "s113-entry-and-test-functions"},
	{CodeAuthority, "construction authority", "implemented", "s114-construction-authority"},
}

// Registered reports whether code is in the table.
func Registered(code string) bool {
	for _, c := range Codes {
		if c.Code == code {
			return true
		}
	}
	return false
}
