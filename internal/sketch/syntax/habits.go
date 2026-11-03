package syntax

// Habit is a Go spelling that the decided grammar refuses with a message
// naming the Ergo form (#220: a host-language habit is not compatibility,
// so the parser says what to write instead). Every habit is S001 at its own
// token. Old Ergo-only spellings (error[…] rows, `wire Name { A = x }`,
// `catch e { … }`, bodiless functions, `uses`, try in operand position) are
// not habits: they get the plain S001 of any stray token (#217).
type Habit struct {
	Spelling string // the Go spelling, as grammar.md lists it
	Message  string // the S001 message; the Ergo form is in backticks
}

// The habits, in grammar.md order. The parser reports each through habit().
const (
	habitPackage = iota
	habitImportGroup
	habitImportQuoted
	habitDefine
	habitVar
	habitConst
	habitIfInit
	habitIncDec
	habitForClause
	habitRange
	habitSwitch
	habitTypeSwitch
	habitTypeAssert
	habitDefault
	habitStruct
	habitInterface
	habitReceiver
	habitErrorResult
	habitGo
	habitDefer
	habitChan
	habitArrow
	habitSelect
	habitMap
	habitVariadic
	habitArray
	habitLabel
	habitGoto
	habitFallthrough
)

var habits = [...]Habit{
	habitPackage:      {"package p", "Ergo has no package clause; the directory names the package"},
	habitImportGroup:  {"import ( … )", "write one import per line: `import net/http`"},
	habitImportQuoted: {`import "p"`, "an import path is unquoted: `import net/http`"},
	habitDefine:       {"x := e", "Ergo declares names with `let x = e`, or `let mut x = e` for a name that is reassigned"},
	habitVar:          {"var x T", "Ergo has no var; declare with `let x = e`, `let x T = e` or `let mut x = e`"},
	habitConst:        {"const x = e", "Ergo has no const; declare with `let x = e`: a literal takes the type its context expects"},
	habitIfInit:       {"if x := f(); c", "an if or match header has no init statement; write `let x = f()` on the line before"},
	habitIncDec:       {"x++, x--", "Ergo has no ++ or --; write `x += 1` or `x -= 1`"},
	habitForClause:    {"for i := 0; i < n; i++", "Ergo has no three-clause for; write `for i in 0..n`"},
	habitRange:        {"for k, v := range xs", "Ergo has no range; write `for x in xs` or `for i, x in xs`"},
	habitSwitch:       {"switch", "Ergo has no switch; write `match x { case p: … }`"},
	habitTypeSwitch:   {"x.(type)", "Ergo has no type switch; write `match x { case T as t: … }`"},
	habitTypeAssert:   {"x.(T)", "Ergo has no type assertions; write `match x { case T as t: … }`"},
	habitDefault:      {"default:", "write `case _:` for the arm that matches the rest"},
	habitStruct:       {"struct", "Ergo has no struct keyword; declare a record with `type Name { fields }`"},
	habitInterface:    {"interface", "Ergo has no interface keyword; declare operations with `type Name { operations }` and a union with `type Name = A | B`"},
	habitReceiver:     {"func (r T) M()", "Ergo has no receivers; make the receiver the first parameter: `func M(r T, …)`"},
	habitErrorResult:  {"(T, error)", "Ergo writes failures after the result: `func F(…) T fails E`"},
	habitGo:           {"go f()", "Ergo has no go statement; start a task with `task.Go() { f() }`"},
	habitDefer:        {"defer f()", "Ergo has no defer; register cleanup with `scope.OnExit`"},
	habitChan:         {"chan T", "Ergo has no channels; use a typed queue"},
	habitArrow:        {"<-ch", "Ergo has no channel operator; use a typed queue"},
	habitSelect:       {"select", "Ergo has no select; race tasks with `task.Race`"},
	habitMap:          {"map[K]V", "Ergo has no map keyword; write `Map[K, V]`"},
	habitVariadic:     {"...T", "Ergo has no variadic parameters; take a slice `[]T`"},
	habitArray:        {"[N]T", "Ergo has no fixed arrays; use a slice `[]T`"},
	habitLabel:        {"L:", "Ergo has no labels; break and continue act on the innermost for"},
	habitGoto:         {"goto", "goto is not part of the language; use structured control flow"},
	habitFallthrough:  {"fallthrough", "fallthrough is not part of the language; list the patterns in one case"},
}

// Habits returns the Go spellings the parser recognises, each with the
// message that names the Ergo form.
func Habits() []Habit { return append([]Habit(nil), habits[:]...) }

// reservedHabit is the habit a reserved word starts, where it stands at the
// start of a declaration, statement, type or expression.
var reservedHabit = map[Token]int{
	CHAN:        habitChan,
	CONST:       habitConst,
	DEFAULT:     habitDefault,
	DEFER:       habitDefer,
	FALLTHROUGH: habitFallthrough,
	GO:          habitGo,
	GOTO:        habitGoto,
	INTERFACE:   habitInterface,
	MAP:         habitMap,
	PACKAGE:     habitPackage,
	RANGE:       habitRange,
	SELECT:      habitSelect,
	STRUCT:      habitStruct,
	SWITCH:      habitSwitch,
	VAR:         habitVar,
}
