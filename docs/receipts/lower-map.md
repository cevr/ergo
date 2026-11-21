# Lower map: which lowerer files the IR path reads (FLIP-map)

Measured at `wf10/ir-red-1` 2e7a7bbdb (batch 21's integrated head 361ec4fb1 plus IR-RED-1), for capstone §10 item 3 (unit FLIP-map). It is S4's port map and RETIRE's delete list.

## How it was measured

`internal/sketch/lower` has 116 Go product files. The IR builder is the 16 `ir*.go` files. This map classifies the other 100.

- The tool (scratch, not committed) parses the package's product files and builds a graph between its top-level declarations by name. A method name refers to every method of that name, so the graph over-approximates.
- The roots are every declaration of an `ir*.go` file. The walk follows references from the roots. It stops at any declaration that writes Go text (a `.line`, `WriteString` or `WriteByte` selector in its body): that is an emitter. When the builder calls one, the emitter is marked `!`. Those are the builder's fallback seams, such as handing a body back to the emitter.
- A declaration the walk reaches is **read by the IR builder** (a shared decider). Any other declaration is **emitter only**.
- A file's class comes from the share of its declaration bytes that the walk reaches:
  - **IR-path**: 80% or more. The file is deciders the IR builder reads, so it is ported with S4.
  - **shared**: between 21% and 79%. Split the file at S4: its deciders port, and its emitters go at RETIRE.
  - **text-only**: 20% or less. It goes at RETIRE.
  - **test/law** marks the law, test and rewrite lowering (tests.go, lawcases.go, machinelaws.go, rewritelaw.go …). IR6 moves their bodies to the IR (test and law bodies since IRG6 r3 and r4); their harness text stays until FLIP rules on it.

Because of the name-based graph, a percentage is an upper bound on how much the IR path reads. A text-only file is reliable: the walk does not reach it even with over-approximation.

## Totals

| class | files |
|---|---:|
| IR-path | 29 |
| shared | 31 |
| text-only | 40 |

## Product users of emitted Go text

These must move to the IR (or to its text view) before FLIP takes the emitter off the product path.

1. **`ergo lib`**: `internal/sketchfront/sketchfront.go:300` calls `lower.LowerLibrary` (library.go, text-only).
2. **`internal/linked`** (SEM1, `wf10/sem1` 63a4a0ed1; on no batch head yet): `internal/linked/linked.go:93` calls `sketchfront.Library`, the same `lower.LowerLibrary` path. `scripts/linked` writes its output into `internal/linked/<name>/<name>.go`.

   For both, capstone §12 decided that after FLIP the Go text is the IR's text view (TXT, `-go-text`, which TestIRTextChecks type-checks and vets) plus the declaration file.

The map also found three more product reads of the emitter that FLIP must rule on:

3. **The source split of a non-IR build**: `splitLowerer(test, ir)` in sketchfront.go:521 calls `lower.LowerSplit` when `req.IR` is false. FLIP removes that branch.
4. **Rewrite checks**: `lower.RewriteChecks` (rewritecheck.go, shared) runs `probeLower`, a text lowering, to learn which laws a site asks for. `sketchfront/rewrite.go:116` and `:281` call it. The question it asks (which rewrite law a site uses) is a decider; it has to be read from the IR builder's rewrite path instead.
5. **Reports**: `sketchfront/inspect.go:76` (the rewrite sites) and `:95` (CopyReport, `ergo build -report copies`) run `lower.Lower` for its side records (`unit.Copies`, the rewrite sites). Both records are deciders' output, so they have to come from the IR build.

`docs/research/pattern-bench/gen` uses `lower.MatcherText` and `MatcherTableText`. It is research, not product.

## Exported entry points

| entry point | file | class |
|---|---|---|
| `LowerIR`, `LowerIRTests`, `LowerIRSplit`, `CensusIR`, `CensusStd` | ir.go | the IR path |
| `Lower`, `Runtime` | module.go | shared (the module and runtime files of both paths) |
| `LowerSplit` | pkgsplit.go | the source path's split. pkgsplit_ir.go is the IR's split, and the two share the placement (`splitPlan`) |
| `LowerTests` | tests.go | the source path's tests (test/law) |
| `LowerLibrary` | library.go | text-only: `ergo lib` and `internal/linked` |
| `RewriteChecks`, `HasRewriteLaws` | rewritecheck.go | shared (see item 4) |
| `ParseLedger`, `FormatLedger`, `LawDigest`, `NewLedgerEvidence`, `LedgerEntries` | ledger.go | IR-path (the law ledger; neither path writes it as text) |
| `MatcherText`, `MatcherTableText` | refinematch.go, pattern_table.go | text-only (research) |
| `RuntimeGoModule`, `MergeGoModules`, `GoModWord` | gomodule.go | shared (go.mod of both paths) |
| `ValidatePath`, `Directive`, `InlineDirective` | lower.go | IR-path |
| `Set…ForTest` (catch switch, host copy, Go error, patterns, releases) | catch_plain.go, gocall.go, pattern.go, static.go | the reference hooks of speed lowerings (memory #1). When a form keeps its reference, the hook moves to the shared decider. |

## Files

The middle column lists the declarations the IR builder reads (cut at 300 characters). The last lists emitter-only declarations, with `!` on an emitter the builder calls.

| file | bytes | IR-reached % | class | read by the IR builder (deciders) | emitter only (! = an emitter the builder calls) |
|---|---:|---:|---|---|---|
| alias.go | 13937 | 100 | IR-path | aliasLowering, borrowResult, callFlow, carries, climb, declaredHere, defsIn, flow, hides, hidesParts, newFlow, reachesResult, resultDeps |  |
| arrays.go | 494 | 100 | IR-path | arrayLen |  |
| blocking.go | 7591 | 100 | IR-path | callerWait, childWait, drainWait, endWait, goDurationName, handleWait, hostBlockingIntrinsic, memberTask, noWait, parentTask, runtimeBinding, runtimeBindings, runtimeCall, settleWait, takesCaller, waitTask |  |
| body.go | 48350 | 35 | shared | at, atSite, block, bound, caseKey, declare, entryPoll, failureValue, fixedName, frame, frameFootprint, framePolls, funcCtx, innerFuncCtx, isInterface, isPlace, loopCtx, newFrame, newFuncCtx, ret, returnsErrorType, stmts, target, temp, typeMatch | !body, !branch, !discard, !fail, !lawResult, !let, !line, !loopBody, !place, !poll, !retExpr, !stmt, !typeMatchBody, !valueMatch, assign, containsBreak, enterSwitch, errReturn, exprStmt, forStmt, joinResult, leaveSwitch, loopBind, rangeStmt, resultSite, returnedTemp, trackResultSite |
| bodycontract.go | 3059 | 100 | IR-path | bindingOf, bodyCatalog, releaseRoutes, runtimeBody, waitKinds |  |
| bounds.go | 20208 | 70 | shared | addSpans, boundFact, boundLimit, constOf, exact, factLowering, intVar, isInt, lenOf, limit, limitOf, noWrap, ordered, rootOf, sliceLen, span, spanOf, stable, subject, term, typeOrNil, uncheckedIndex, uncheckedOK, uncheckedSlice, unstableVars, within | addBound, assume, assumeLet, assumeRange, factMark, factReset, kill, killAssigned, killWritten, oneVar, rootVar, uintOf |
| builders.go | 3726 | 3 | text-only | isOps | builtText, genBuilt, opBuilder, stdBuilt |
| calls.go | 69208 | 28 | shared | callArgs, callArgsAddr, callIdent, callee, context, contextBuilder, contextSet, declIdent, declaredContext, envExpr, failsHere, funcValue, hostBase, hostCallback, hostCalled, hostPkg, hostTarget, hostValue, isString, localContext, ownsResources, paramsMention, recordCall, recordDecl, siteValue, stdContext, stdValue, typeArgs, … | !acquiredSet, !call, !emitContext, !operations, !with, buildProvider, buildProviders, builtinCall, builtinHelper, builtinHelpers, builtinLowering, callStmt, callTable, callText, hostCall, hostFallible, hostImportNames, hostStmt, identify, mentionsTypeParam, publishProvider, scopeCall, taskDestArgs, withExpr |
| canon.go | 12034 | 97 | IR-path | canonical, canonicalize, declLists, declOrder, funcs, lawRuns, laws, less, providerName, sourceIndex, sourceOf, sources, tests, types | initOrder |
| carry.go | 9396 | 100 | IR-path | carriedUnions, carries, carryPayload, carryResults, flatByUse, paramCarry, passCarries, payloadCarry, unionBound, useBytes, useCarries |  |
| catch_plain.go | 4591 | 0 | text-only |  | SetCatchSwitchForTest, catchBinding, catchSwitchAlways, goFailing, goIdent, plainCatch, plainCatchBody |
| cells.go | 4411 | 26 | shared | cellCopy, cellLowering, cellSite | cellWindow, noteCell |
| closures.go | 5102 | 69 | shared | callerEnv, captureLowering, capturedEnv, childEnv, envBinding, envClosure, plainEnv | !snapshot |
| construction.go | 6494 | 91 | IR-path | at, constructionOpen, openConstruction | constructionWhy |
| context.go | 5462 | 4 | text-only | contextNamespace | contextCall, contextWith |
| contextvalues.go | 3076 | 0 | text-only |  | !contextValue, installWith |
| decls.go | 60160 | 46 | shared | constraintOf, decl, definedErrorOver, emitFunc, emitFuncBody, env, envCap, envField, envTask, funcDecl, funcDecl, goErrorDecl, intrinsic, isBasic, isMapOf, isPreludeType, isSliceOf, kindAlias, kindDefined, kindMachine, kindOps, kindRecord, kindUnion, letRead, methodReceiver, optionReturn, optionVariants, packageLet, packageVar… | !addInit, !emitBody, !emitType, !letDecl, !packagePattern, constLet, constraint, declareName, declareNames, directive, directiveAt, envType, hasInits, initEnv, initFunc, initOrder, methodsOf, patternVars, recvType, typeParams, typeSetDecl, unionMarker, withsOf |
| defect.go | 2227 | 22 | shared | unreachableCall | unreachableArgs, unreachableStmt, unreachableValue |
| drop.go | 4657 | 0 | text-only |  | droppedKey, droppedName, droppedSite, reserveDropped |
| embed.go | 2131 | 32 | shared | assetFiles | !packageEmbed |
| equal.go | 9720 | 5 | text-only | typedIn, underEqual | equalBase, equalCall, equalFunc, equalKey, equalOp, equalType, needsEqual, reserveEquals |
| expr.go | 47229 | 57 | shared | arm, assigned, bareRecord, composite, convert, convertAt, covered, elements, emits, expr, expr1, exprOf, fallibleExpr, fieldName, funcLit, funcLitClosure, handsOff, ident, index, isConst, methodOf, noValue, operand, operands, operation, sliceExpr, tried, try, typeOf, typed, unparen, value | !armBody, !binary, !catch, !propagate, !selector, !spill, !unary, !update, binaryOf, caughtValue, elemAt, flatPatternCatch, isGoPath, isLiteralText, patternCatch, rangeValue, retTail, simpleCatch, spread |
| flatunion.go | 41590 | 84 | IR-path | arrayKey, asName, basicSize, flatBail, flatNarrow, flatShape, flatUnion, flatUnionOf, flatUnionSet, flatValue, flatVariantValue, goLayout, head, instance, isOptionVariant, isSum, layoutOf, mapKey, mentioned, mentionsGoRef, nicheAdmitted, nicheElem, nicheOff, ofName, optionCycle, optionLoop, optionNone, optionShape, optionSome, … | !nilTagHelper, flatDecl, lowerAgain, sharedDecl, sharedName, tagSwitch, tagSwitchOf |
| floatconv.go | 1773 | 0 | text-only |  | float64Of, floatToInteger, floatToIntegerHelpers |
| forms.go | 15347 | 38 | shared | constFold, constLike, constUses, formLowering, formsOn, freshKey, leaves | aliasLet, catchAlias, isFresh, mapStore, markFresh, renameTail, soleFailure |
| getmatch.go | 9269 | 39 | shared | getArm, getArmsOf, getLowering, getPattern, preludeGet, takeArm | getArms, getArmsLower, getCopy, getMatch |
| gobody.go | 8841 | 100 | IR-path | appenderExtern, goBodies, goBodyAppends, goBodyMakes, goBodyView, madeResult, madeValue, pureV1, sameExpr, viewExtern |  |
| gocall.go | 21325 | 10 | text-only | goErrorEager, goErrorOf, goExternCall, goImportCall, goShared, hostCopyAlways, hostPkgsIn, hostRecv | !goErrorOf, SetGoErrorEagerForTest, SetHostCopyAlwaysForTest, afterGoContext, freshGoResults, goCall, goContext, goErrorArgs, goErrorCall, goErrorFrom, goExtern, goResult, goSeqArgs, goShares, goSig, hostFuncRef, hostNamed, isGoRef, isOptionType, optionElem, seqFromGo, someWhen |
| gocallback.go | 5254 | 0 | text-only |  | goCallback, goCallbackIn, goCallbackOut, goCallbackType, goCallbacks, goUntunnel |
| gointerface.go | 4695 | 12 | text-only | goInterfaceKey, hostSourced | !goInterface, goFails |
| gomodule.go | 9431 | 25 | shared | GoModule, GoReplace, GoRequire, covers, goModFile | !goModFileAt, GoConflict, GoModFile, GoModWord, MergeGoModules, RuntimeGoModule, newerGo, replacementTarget, runtimeSource |
| growth.go | 4783 | 57 | shared | coldList, enterList, growthAcct, sitePath | functionGrowth, grow, growthLowering, inlineGrowth |
| handover.go | 22656 | 79 | shared | CopyReport, CopySite, appendSite, appenderConsumes, checkedOwnedForm, copySite, deadField, disjointRead, endingReturn, fieldNames, fieldOwned, fieldPath, freshAll, freshCall, lineageSites, ownedFormAt, passedByName, settleLineage, settleNamedTails, spreads, takenField | copyReport, handoverAppend, lineageDecl, lineageOK, lineageToken, lineagesKey, ownFieldOK |
| header.go | 3127 | 79 | shared | closing, guardHeader, typeNameLiteral | headerValue |
| hot.go | 2518 | 0 | text-only |  | hotFunc, loopCalled |
| inforce.go | 10499 | 56 | shared | allPkgs, capsKey, capsOf, held, heldArg, heldEdge, heldInfo, isHeld, rowArgElems | !capsType, !envCopier, envWith, holdsRows, inForce, reserveHeld |
| inline.go | 35711 | 3 | text-only | inlineKey, inlineRet, inlineSite, inlineTag, litSub, onLoopPath, tagPath | !writeDir, boxedResult, closeInline, copyGrowth, fieldBudget, generatedDir, goInlineBudget, goLeaf, goSizeBudget, growthOf, inlineArgs, inlineAt, inlineBody, inlineBudget, inlineCall, inlineCostLowering, inlineDepth, inlineFrame, inlineLit, inlineLowering, inlineRecursive, inlineReturn, inlineRule, inlineSize, inlineTable, … |
| interp.go | 6953 | 100 | IR-path | floatTextMax, intTextMax, interpolated, segment, strconvPkg, text |  |
| keeps.go | 7874 | 100 | IR-path | callWrites, keepFlow, keepLowering, keeps, matchBinds, names, underlyingOf, unkept |  |
| known.go | 20543 | 84 | IR-path | callOperands, callbackLowering, callbackParams, knownBody, knownBodyCall, knownClosed, knownDeciding, knownNo, knownOperands, knownState, knownStats, knownVariant, knownYes, paramVars, plainLit, plainSig, pureLit | knownKey, knownName, knownSite, plainFuncType, reserveKnown, statsHook |
| knownmatch.go | 6373 | 11 | text-only | knownMatch | foldLowering, knownGeneral, knownMatchFor, knownReturn, knownTake |
| lawcases.go | 11399 | 0 | text-only, test/law |  | enumCap, enumCases, enumRun, enumValues, listedRun, listedRuns, listedTest, listedValue, planValue, provedText |
| lawcontexts.go | 4211 | 0 | text-only, test/law |  | lawContextRuns |
| ledger.go | 20897 | 64 | shared, test/law | Applied, Key, LawDigest, LawRunVersion, LedgerEntry, LedgerEvidence, LedgerFile, LedgerNames, Line, Of, ParseLedger, RewriteSite, Sites, decl, declText, digest, digester, evidenceFor, fn, index, law, ledgerKey, let, letDecl, member, parseEntry, provider, rule, splitInstance, tableFuncs, typ, walk | !Admit, FormatLedger, HasRules, LawPackage, LedgerEntries, NewLedgerEvidence, Notes, ProviderPackage, Read, instanceText, record |
| library.go | 10615 | 0 | text-only, text product |  | LowerLibrary, export, exportReason, exports, exportsOf, goPackage, goSpells, hostProvides, initOnce, nameExports |
| literal.go | 13713 | 10 | text-only | litExpr | !litFunc, ergoType, importName, litByConstructor, litKey, litTypes |
| loops.go | 16634 | 0 | text-only |  | collectIdiom, fillLowering, hasField, holdsByValue, invariant, loopIdiom, loopIdiomHead, loopIdiomOf, loopLowering, offsetOf, sameSyntax, sliceElem, unKey, zeroComparable |
| lower.go | 6996 | 100 | IR-path | Directive, InlineDirective, Source, Unit, Validate, ValidatePath, validateFiles |  |
| machine.go | 17350 | 53 | shared | machineAfter, machineDecl, machineField, machineInitial, machineNone, machineOf, machineStart, machineWork, positiveTimers | machineGraph, machineInit, machinePlan, machinePure, namesIdent |
| machinelaws.go | 32172 | 0 | text-only, test/law |  | callText, harnessReachGo, importAs, keyFunc, keyName, keyStmt, keyTypes, keyWhy, machineLawRuns, quoteAll, reachRuns, reachTypes |
| match.go | 13814 | 14 | text-only | declaredUnion, narrow, patBind, simpleTypeMatch, tupleField, underlying | bindArm, flatPatternMatch, isHelper, letPattern, matchArms, pattern, patternMatch, typeTest |
| matchtree.go | 22780 | 72 | shared | bail, bindAll, compile, containsString, count, expand, fallback, fieldCells, head, headConst, headCtor, headKind, headSplit, headWild, matchLowering, matchTree, matchTreeTest, mcase, mcol, mhead, mleaf, mnode, mrow, patternSize, recordColumns, specialize, splice, treeArm, treeBail, treeBuilder, walk, wilds | !leaf, !node, emitMatchTree, treeEmit, treeExit, treeMark |
| module.go | 37140 | 59 | shared | Error, File, Refusal, Refused, Runtime, goVersion, guard, ice, lowerer, lowererOf, moduleFiles, newBuildLowerer, pkgEntry, pkgKey, pkgName, programModule, readVars, refusal, refuse, rt, runtimeDir, runtimeImport, runtimeModule, stop, taskImport, taskPkg, timePkg, unreachable, unreadIn | !entry, !program, Lower, anchorLines, drainPending, entryError, fillInitSlot, hostDecl, hostFields, initSlot, initSlotLine, lowerPackage, mainFunc, newLowerer, readsOf, rootTask, sortedRefusals, withRecord |
| moveout.go | 12981 | 92 | IR-path | copiedOnce, decideMoveOuts, entryWritten, fieldPath, keepsNothing, matchBinds, matchOf, matchedGet, moveOut, moveOutHolds, moveOutsIn, sameKey, stmtLists, storedAsEntry | markEntryWrites |
| moves.go | 5437 | 100 | IR-path | decideMoves, placeOf, plainFresh, resliceOf, takeOf |  |
| names.go | 3537 | 100 | IR-path | alloc, goReserved, memberName, namer, namerWork, newNamer, taken |  |
| naming.go | 16752 | 33 | shared | adapterKey, adapterName, contextPackages, convParams, libraryPackages, opsSource, paramSource | generatedEntry, nameAll, quiet, sortGenerated |
| owners.go | 3816 | 100 | IR-path | bodyPkg, buildView, errorType, errorTypeIn, errorTypesRead, readsFact, viewOf |  |
| pattern.go | 2521 | 0 | text-only |  | SetPatternLoweringForTest, emitNative, nativeOf, patternLowering |
| pattern_table.go | 5479 | 0 | text-only |  | MatcherTableText, PatternTableStates, classesOf, patternClasses, patternTableKey |
| pkgsplit.go | 32808 | 13 | text-only | SplitReport, can, isMainGo, litExpr, litForeign, litStruct, lower, place, splitGroup, splitPkg, splitPlan, start, stdOrigins | !splitMain, LowerSplit, directivePath, exported, goIdentOf, goPackageName, goPathOf, hasEmbed, importLocal, isGoInit, litKind, lowerSplit, recvBase, sharedDir, splitDir, splitFiles, splitPackages, stdDir, stdImports |
| pkgsplit_ir.go | 9419 | 50 | shared | add, files, irHash, irSplit, splitDefs, walk | foreign, foreignDecls, freshName, importPaths, newIRSplit |
| provider.go | 7837 | 1 | text-only | knownProv | findProvParam, knownProvider, provFuncs, provParam, provSpecBase, provSpecKey, provType, providerLowering, providerOf |
| pure.go | 12891 | 100 | IR-path | envLead, goBasicName, hostProtocols, isPure, meetsHostProtocol, protocolFreeName, pureCandidate, pureIntrinsic, pureStdBody |  |
| readonly.go | 6730 | 100 | IR-path | callFree, placeTaken, readAtOnce, readOnlyUse, readingCall, readingLoop |  |
| record.go | 12702 | 9 | text-only | knownRec, specCopy, specKey | findRecordParam, inlineField, knownRecord, recordCallText, recordLowering, recordParam, reserveSpecs, sourcedMachines, specAt |
| refinematch.go | 5667 | 0 | text-only |  | MatcherText, isPlainString, patternCall, patternKey, patternMatcher, patternTable, reservePatterns, runeLit, spanTest |
| refown.go | 17750 | 100 | IR-path | adaptInPlace, decideSites, frameResultsFresh, handParams, inPlaceLit, inPlaceOf, mayShare, ownedForm, ownedFormOf, ownedRefCall, ownedSite, reaches, refCall, refSite, refSiteOf, resultFresh, returnNode |  |
| release.go | 43834 | 36 | shared | acquireFlow, bodyOwns, callerRelease, closeTypeArgs, entersRelease, fieldNames, frameRelease, glue, glueGen, glueOn, handoff, hostRelease, isClose, notRelease, ownBinding, ownRelease, owning, ownsNode, ownsParams, ownsReplaced, ownsValues, part, partsFrom, providerRelease, release, releaseRoute, resultRelease, scoped, underOf | !glueWith, !handParts, !own, !ownTemp, !ownVar, !parts, !patternOwners, !releaseAt, !sourceReturn, !takeField, armHeldVar, armKey, armValueEnd, armValueVar, armYield, closeBody, handBase, holdTaken, lentYield, ownReplaced, providerCloser, reassignOwned, reassigned, releaseName, settleTaken, stdOp, withClose |
| reserve.go | 8595 | 6 | text-only | mentions | !reserve, assignsTo, intLiteral, plainPath, rangeCount, reserveCall, reserveLowering, reserveRange, reserveRangeSpan, reserveTarget, selfAppend |
| rewrite.go | 18902 | 62 | shared | RewriteEvidence, factsAt, newRewriter, postForm, postLaw, rewritable, rewriteFoldBudget, rewriteLowering, rewriteWorkPerCall, rewriter, run, rwArg, rwCall, rwFact, rwPlace, rwRule, sideCall, try | !rewriteWindow, rewriteCall, windowCall |
| rewritecheck.go | 9932 | 33 | shared, test/law | Admit, Error, RefutedRewriteError, checkedEvidence, constArgs, evidenceKey, instAsked, lawAsked, provTitle | !instanceKey, HasRewriteLaws, RewriteCheckSeed, RewriteChecks, importLawRuns, importRules, probeLower, rewriteCheckRounds |
| rewritelaw.go | 19788 | 3 | text-only, test/law | RewriteInstance, constGo | drawPrefix, postRun, rewriteCases, rewriteDecl, rewriteFunc, rewriteGuard, rewriteInstanceCases, rewriteInstances, rewriteObserve, rewritePrefix, rewriteRuns, rewriteSide, rewriteSteps |
| rowslot.go | 4005 | 100 | IR-path | goArg, goParam, hasGoRow, intoSlot, slotType |  |
| scalar.go | 14933 | 13 | text-only | joinCall, scalarIdent, scalarLocal, scalarOf | !gather, !scalarLet, !scatter, joinReturn, joinSite, scalarArgs, scalarCandidates, scalarDeclare, scalarLowering, scalarTags, scalarVars, scalarWrite |
| script.go | 11279 | 20 | text-only, test/law | providerCall, scriptFields, scriptLit | !scriptType, reserveScripts, scriptBase, scriptOperations, scriptSig |
| sealedops.go | 5994 | 0 | text-only |  | sealedMethod, sealedOperations |
| shortcuts.go | 6546 | 100 | IR-path | checkedClass, detectorClass, memoryClass, shortcut, shortcutNamed, shortcuts, takes, took, trust, valueClass |  |
| sources.go | 14345 | 74 | shared | at, enter, importOrder, inStdPkg, isSourced, leave, libraries, locate, lowerState, paramFile, queue, reachStdLet, setCur, shippedLedger, sourceOf, sourcedLibs, sourcedPkgs, srcFile, state, stdFile, stdFileOf, stdLet, within | eachDecl, eachStdLet, missingImport, nameStdLet, nodeID, sourceLess, stdLetPkgs |
| static.go | 10083 | 14 | text-only | staticBit, staticFrame, staticHand, staticSlot, staticState | SetReleaseLoweringForTest, closeStatic, openStatic, releaseLowering, staticMarker, staticOwn, staticPlan, staticRelease, takesHeld |
| std.go | 35851 | 83 | IR-path | adaptFunc, adaptPlainFunc, adaptTaskFunc, argSig, bindDeferred, bindFunc, bodyNeedsFrame, bodyNeedsScope, commaOk, defersCall, frameScope, goFallible, goValue, hasHidden, hiddenArgs, namedResults, needsFrame, opBody, opCall, programOpCall, runtimeType, schedulerCap, stdBody, stdCall, stdResult, stdTypeArgs, uniformBody | !adaptFuncs, !openFrame, isRuntimeType, methodValue, opCallExpr, returnFallible |
| storeread.go | 7430 | 100 | IR-path | containers, keptRead, ptrRecv, reslices, reslicesRoot, storageWritten, writesNothing |  |
| tabulate.go | 8943 | 2 | text-only, test/law | tabInfo, tabParam | findTab, refFree, reserveTabs, tabAt, tabDecl, tabLowering, tabMax, tabParamOf, tabulable |
| tails.go | 9269 | 19 | text-only | assignedTo, dest, destKind, tailFallible | !blockBody, !branchExpr, !noCase, destAssign, destReturn, destStmt, ifYield, needsDefault, yieldList, yieldStmt |
| taskcapture.go | 4233 | 30 | shared | captureNodes, captureOwners, movingRootArg | !taskSnapshot, captureCall, handCaptures, taskCaptureRoot |
| tests.go | 81993 | 8 | text-only, test/law | assertShown, bindingUnrun, helper, isStringLike, lawGenericOwner, lawLowered, printable, runLaws, siteText, testHelpers, testLowering, typedExpr | !assert, !lawRuns, !testDecl, LowerTests, assertCompare, bindingLabel, genConstructor, genExpr, genFunc, genName, genNamed, genPick, genRefined, genRoots, harness, harnessGo, int64Bits, lawContexts, lawCounts, lawDecl, lawFrame, lawFunc, lawOwnOf, lawSlotParam, lawTitle, lenCap, nameTests, newTestLowerer, providerTitle, replayL… |
| text.go | 13557 | 0 | text-only, text product |  | errorMethod, instancesOf, textExpr, textFields, textFunc, textKey, textRoots, textTypes, textVariants |
| types.go | 24961 | 84 | IR-path | boundType, constant, defaultValue, exactFloat, fieldDefaults, foreignOwner, freeTypeParams, funcType, goType, holdState, holdsMap, holdsNo, holdsUnknown, holdsVisiting, holdsYes, isErrorValue, isInteger, resultList, tupleDefaults, typeArgs, typeCtx, typeKey, valueResults, zero | !defaultFunc, defaultBase, defaultKey, methodSig, reserveDefaults |
| unfold.go | 6912 | 100 | IR-path | branchDef, rewriteUnfoldPerCall, straightLine, unfoldDef |  |
| unreachable.go | 3643 | 100 | IR-path | ruleAccepted, ruleCaughtTwice, ruleCoveredCase, ruleFuncBody, ruleGenericValue, ruleLetNames, ruleLiteralCases, ruleMainSignature, ruleMemberNames, ruleNamingPass, ruleOneResult, ruleParser, ruleSimpleCatch, ruleSimpleMatch, ruleVariantRecord |  |
| valueproof.go | 13496 | 0 | text-only, test/law |  | lawProvider, proofBodyWhy, proofEach, proofLists, proofStateText, valueProof |
| values.go | 96614 | 91 | IR-path | analyzeValues, argOf, argsUnshared, basicResult, chainOf, children, classify, cloneDeep, cloneOf, cloneValue, consumable, consumedAt, consumedParams, consumedUse, consumes, containsNode, copyCovers, copyCoversSeen, copyLowering, decide, dotCall, elemOf, enclosingCall, entryRule, entryShared, failsAfter, frameOfReturn, frameOf… | !cloneFunc, cloneBase, cloneKey, inside, ownedKey, ownedWrapper, reserveClones, reserveOwned |
| wire.go | 32343 | 23 | shared | instArg, wireCallFunc, wireCodecFuncs, wireCodecSigs, wireCodecType, wireCtx, wireDirectFuncs, wireDirectType, wireEncode, wireEncodeWith, wireFieldType, wireInline, wireOf, wireUnder, wireWitness, wireWits | !wireDecode, !wireFunc, intRange, reserveWires, uintMax, wireBase, wireCall, wireDecodeBody, wireEncodeBody, wireFailure, wireKey, wireKind, wireParseReturn |
| wiredirect.go | 7134 | 49 | shared | basic, dir, directDir, directField, key, mapKey, open, param, referenceEvents, tag, wireEvents | close, collectDirectWriters, directIndex, directSource, directWriter, wireDirect, wireLowering |
| wirefields.go | 6421 | 37 | shared | wireFieldWitness, wrapperParts, wrapperValue | paramField, wireFieldCount, wireFields, wireNullableDecode, wireNullableEncode |
| wiretext.go | 20880 | 89 | IR-path | Admit, cellBlock, cellBranch, cellEntryCall, cellEvents, cellEventsFrom, cellFunc, cellOf, cellRewriter, cellRun, cellVariant, cellWriter, cellWriterOf, directGoType, eventConst, eventLine, idempotent, newShippedEvidence, oneLineBlock, sameCall, shippedEvidence | cellEntryKey, cellOpCall, cellOperand |
| wirevalue.go | 5439 | 0 | text-only |  | barePart, memberParts, numEncode, wireMembersDecode, wireMembersEncode, wireNumDecode, wireNumEncode, wireValueDecode, wireValueEncode |
| wirewrite.go | 2128 | 0 | text-only |  | wireOwnWrite |
| witness.go | 9488 | 69 | shared | isWireParam, witnessArgs, witnessCall, witnessOp, witnessParams, witnessValue | selfSig, witnessMethod, witnessType |
| witspec.go | 7646 | 4 | text-only | staticOp | btoi, knownWitness, mentionsTypeParamList, reserveWitSpecs, staticWitness, witArg, witKey, witSpecParam, witnessLowering |
