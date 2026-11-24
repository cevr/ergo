package lint

// The built-in presets. Recommended is the base: the rules on by default,
// each with a published precision at Bar or above (precision.go;
// TestDefaultRulesMeetTheBar). No rule reaches the bar yet, so it is
// empty. Justifiers, Strict and Layout are opt-in (--preset or extends):
// justifiers holds the justifier rules (legal code that must say why;
// they were the base until their read sample, 2026-11-17, put them under
// the bar), strict the complexity caps (q149) and the style rules, layout
// the owner's Go layout (AGENTS.md), change the rules of a change, which
// report only under --diff (anti-slop AS6).
const (
	Recommended = "recommended"
	Justifiers  = "justifiers"
	Strict      = "strict"
	Layout      = "layout"
	// Advisory is the opt-in preset of rules a reader judges: hints that
	// never change the exit code by their severity (spec §4 point 2).
	Advisory = "advisory"
	// Speed is the opt-in preset of hints about work a loop repeats or a
	// call copies (catalog §3, PF5 to PF18).
	Speed  = "speed"
	Change = "change"
)

// builtinRules are every built-in rule.
var builtinRules = []*Rule{
	silentCatch, foreignErrorPublic, foreignWhereStd, deprecated, unboundedCollect, raceForTimeout,
	cognitiveComplexity, cyclomaticComplexity, halsteadDifficulty, knownValueWidening, bannedWords,
	withAtEntry, conceptFileNames,
	unusedDeclaration, unusedTypeParameter, duplicateTest, duplicateShape, identicalArms, unbuiltVariant,
	forwardingFunc, loneProvider, modeParameter, stringlyState, etaMethodValue,
	testWaitsOnTime,
	defaultOnFailure, repeatedPureCall, guardTheTypeProves, repeatedGuard,
	foreignOutsideAdapter, capabilityOutsideOwner, consoleOutsideEntry, testReadsUnexported,
	causeDropped, failureAsVariant, vacuousTest, parameterClump, constantArgument, longParameterList,
	derivedField, optionFollowsVariant, takeOneByLaw, independentWaits,
	constantRestated, repeatedCase,
	fileCycle, layer, domainImportsImplementation, hiddenDependency, wideObservedInterface, longFile, wideDirectory, foreignRecordFieldWrite, productionUsesTestSupport, seamEntrypoints, siblingFolderCycle, declarationTangle,
	narrationComment, commentedCode, noComments,
	snapshotArgument, repeatedTraversal, quadraticScan, concatInLoop, largeValueCopy,
	addedClone, scatteredEdit,
	selfAssignment, signDecided, wireKeyQuote,
}

// builtinPresets are the built-in presets over builtinRules.
func builtinPresets() []Preset {
	on := func(rules ...*Rule) map[string]Setting {
		out := map[string]Setting{}
		for _, r := range rules {
			out[r.Name] = Setting{}
		}
		return out
	}
	return []Preset{
		{
			Name:  Recommended,
			Doc:   "The rules on by default: each has a published precision of at least 0.80.",
			Rules: on(),
		},
		{
			Name:  Justifiers,
			Doc:   "The justifier rules: legal code that must say why.",
			Rules: on(silentCatch, foreignErrorPublic, foreignWhereStd, deprecated, unboundedCollect, raceForTimeout),
		},
		{
			Name: Strict,
			Doc:  "Complexity caps (cognitive and cyclomatic 21, Halstead difficulty 79), style (known-value widening, banned words) and dead or duplicate code: unused declarations and type parameters, duplicate tests and shapes, identical arms, unbuilt variants; and needless indirection: forwarding functions, lone providers, mode parameters, stringly state, a literal that is a method value; tests that wait by letting time pass; and defensive code: defaults on any failure, repeated pure calls, guards a refined type proves, and the same guard in three functions; and the anti-slop family: a dropped cause, a failure returned as a value, a test that cannot fail, parameter clumps, constant arguments and long parameter lists; and the vet rules: a self-assignment, a comparison its sign decides, a wire key written as a Go struct tag.",
			Rules: on(cognitiveComplexity, cyclomaticComplexity, halsteadDifficulty, knownValueWidening, bannedWords,
				unusedDeclaration, unusedTypeParameter, duplicateTest, duplicateShape, identicalArms, unbuiltVariant,
				forwardingFunc, loneProvider, modeParameter, stringlyState, etaMethodValue, testWaitsOnTime,
				defaultOnFailure, repeatedPureCall, guardTheTypeProves, repeatedGuard,
				causeDropped, failureAsVariant, vacuousTest, parameterClump, constantArgument, longParameterList,
				derivedField, optionFollowsVariant, constantRestated, repeatedCase,
				selfAssignment, signDecided, wireKeyQuote),
		},
		{
			Name:  Layout,
			Doc:   "The Go layout of AGENTS.md: implementations chosen only at the entry, files named by concept, Go code only in adapters, capabilities run only by their owners, Console kept out of domain packages, tests through the exported functions, and from the graph rows: imports down a declared layering, implementations imported only by an entry, and no name reached through a re-export of a package not imported; as advisory hints: wide-observed-interface, long-file, wide-directory, foreign-record-field-write, production-uses-test-support, seam-entrypoints, sibling-folder-cycle, and declaration-tangle.",
			Rules: on(withAtEntry, conceptFileNames, foreignOutsideAdapter, capabilityOutsideOwner, consoleOutsideEntry, testReadsUnexported, layer, domainImportsImplementation, hiddenDependency, wideObservedInterface, longFile, wideDirectory, foreignRecordFieldWrite, productionUsesTestSupport, seamEntrypoints, siblingFolderCycle, declarationTangle),
		},
		{
			Name:  Advisory,
			Doc:   "Comments a reader would cut, as hints: narration that repeats the code line under it, and code kept in a comment. Opt-in; precision 0.00 until a read sample reaches 0.80.",
			Rules: on(narrationComment, commentedCode),
		},
		{
			Name:  Speed,
			Doc:   "Hints about work a loop repeats or a call copies: a *s argument the lowering copies, a second walk of one collection, a walk inside a walk of it, a string grown by + in a loop, a big element copied every turn, a whole result built to take the one element a law says another function gives, two waits in a row with disjoint footprints. Opt-in; no precision published until a read sample reaches 0.80.",
			Rules: on(snapshotArgument, repeatedTraversal, quadraticScan, concatInLoop, largeValueCopy, takeOneByLaw, independentWaits),
		},
		{
			Name:  Change,
			Doc:   "The rules of a change, read under --diff: a function the change adds that the program has, and one edit added in three or more packages.",
			Rules: on(addedClone, scatteredEdit),
		},
	}
}

// Builtin is the registry of the built-in rules and presets.
func Builtin() *Registry {
	r, err := NewRegistry(builtinRules, builtinPresets(), Recommended)
	if err != nil {
		panic(err)
	}
	return r
}

// BasePreset is the name and rules of the registry's base preset.
func (r *Registry) BasePreset() Preset { return r.presets[r.base] }
