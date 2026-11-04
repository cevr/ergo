package lower

// The rules that make a lowering site unreachable (unreachable, module.go).
// A site whose form the checker or the parser refuses is an internal
// compiler error that names the rule, never a refusal that waits for a
// later stage. Each rule that names a diagnostic code has a negative
// fixture in unreachableFixtures (TestUnreachableRulesRefuse) that shows
// the rule refusing the form; a rule of the lowering's own invariants
// names the function that keeps it.
const (
	// ruleOneResult: a signature has at most one result type, which may be
	// a tuple (196t), and a return has at most one value.
	ruleOneResult = `the parser's S001 at a second result or a second returned value (a function has at most one result type, which may be a tuple; grammar.md, Declarations 7)`
	// ruleLetNames: a let of several names takes apart one tuple of as many
	// elements (196t).
	ruleLetNames = `the checker's S108 "N names for M values" (a let of several names takes apart one tuple of as many elements, 196t)`
	// ruleCoveredCase: a case that earlier cases cover never runs.
	ruleCoveredCase = `the checker's S108 "this case is already covered by the earlier case … (unreachable)" (D-B4)`
	// ruleCaughtTwice: a catch handles each failure type once.
	ruleCaughtTwice = `the checker's S108 "… is caught twice" (rows.md, Catch 2)`
	// ruleGenericValue: a generic function value is an explicit instance.
	ruleGenericValue = `the checker's S108 "the generic function F is only called or instantiated" (D-E1)`
	// ruleVariantRecord: a union's variants are records of its package.
	ruleVariantRecord = `the checker's S108 "a union variant is a record declared in this package" (types.md, Unions 1)`
	// ruleFuncBody: a function that is not an extern has a body.
	ruleFuncBody = `the parser's S001 "expected the function body" (a function that is not an extern has a body)`
	// ruleMainSignature: main takes nothing and returns nothing.
	ruleMainSignature = `the checker's S113 "main takes no parameters" and "main returns no value" (rows.md, Capability rows 4)`

	// ruleParser: the decided parser builds no such node (an init
	// statement, a label, a catch binding, a Go operator, an else that is
	// neither a block nor an if).
	ruleParser = `the decided parser, which builds no such node`
	// ruleLiteralCases: a value match is a match whose cases are all
	// literals (syntax.literalCases); any other match is a pattern match.
	ruleLiteralCases = `the parser, which builds a value match only from literal cases (literalCases)`
	// ruleSimpleMatch and ruleSimpleCatch: the fast lowering of a match or
	// a catch takes only the patterns its guard admits; every other match
	// goes to patternMatch, every other catch to patternCatch.
	ruleSimpleMatch = `simpleTypeMatch (match.go), which sends every other pattern to patternMatch`
	ruleSimpleCatch = `simpleCatch (expr.go), which sends every other pattern to patternCatch`
	// ruleAccepted: every form the checker admits has a case in the
	// lowering (TestAcceptedNodesLower lowers the whole corpus).
	ruleAccepted = `the checker, which admits no other form here (TestAcceptedNodesLower)`
	// ruleMemberNames: the checker refuses two members of one name, and a
	// Go keyword is never an Ergo name (memberName renames only keywords).
	ruleMemberNames = `the checker's one name per member and the parser's reserved words (memberName renames only a Go keyword)`
	// ruleNamingPass: the naming pass names every package-scope
	// declaration before any body is lowered.
	ruleNamingPass = `the naming pass (naming.go), which names every package-scope declaration first`
)
