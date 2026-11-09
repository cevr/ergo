package lower

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// runtimeBindings is the catalog of std Go bindings. runtimeCall is their
// single emitter. A new binding fails closed until its waiting policy is
// stated here; the blocking property derives its primitive axis from this
// catalog. Its release routes are the release owner's (release.go); the
// mask property derives its release axis from them.
type waitTask uint8

const (
	noWait     waitTask = iota
	callerWait          // the hidden *rt.Task, from the invocation environment
	handleWait          // the handle's owner; Move changes the owning frame, not task
	drainWait           // uninterruptible stop and completed drain
	// childWait is the hidden *rt.Task of the invocation environment as the
	// parent of the children the body starts and joins (timeout.After,
	// through rt.AfterSpan). It parks on no primitive of its own: the
	// children's exits and the parent's stop end its wait, and the
	// runtime/v1 laws of After (timeout_test.go) are its native property.
	childWait
	// memberTask is the hidden *rt.Task of the invocation environment, which
	// the body makes the first member of an auto clock's domain
	// (testing.NewVirtualClock); the body never waits on it.
	memberTask
)

type runtimeBinding struct {
	wait waitTask
	// release is the binding's release route (release.go), notRelease for
	// every binding that is not an entry into runtime/v1's release boundary.
	release releaseRoute
	// A host-provider bridge erases one ordinary, zero-field provider
	// operand. The Go body's hidden parameters still come from BodyOf.
	hostTarget, provider string
	// reads marks a binding that reads the invocation task's context
	// values (runtime.md, G1): it takes the task and reading package as
	// hidden arguments and never waits.
	reads bool
}

// takesCaller reports a binding whose Go body receives the invocation's
// task as its hidden CallerTask argument.
func (b runtimeBinding) takesCaller() bool {
	return b.wait == callerWait || b.wait == childWait || b.wait == memberTask || b.release == callerRelease || b.reads
}

var runtimeBindings = map[string]runtimeBinding{
	"Sleep":        {wait: callerWait, hostTarget: runtimeModule + "/runtime/rt.Sleep", provider: "LiveScheduler"},
	"Task.Wait":    {wait: handleWait},
	"Task.Stop":    {wait: drainWait},
	"Queue.Push":   {wait: callerWait},
	"Queue.Pop":    {wait: callerWait},
	"Queue.Values": {wait: callerWait},
	"Queue.Close":  {},
	"Queue.Poll":   {},
	"Queue.Len":    {},
	"Go":           {},
	// timeout.After joins its body and deadline children, whichever ends
	// first, as their parent (runtime.md, Timeouts).
	"After":        {wait: childWait},
	"OnExit":       {release: frameRelease},
	"Release":      {release: callerRelease},
	"New":          {},
	"WithCapacity": {},
	// tasks.Transform joins its children, whichever ends first, as their
	// parent (stdlib.md, Tasks; rt.MapSpan).
	"TasksTransform": {wait: childWait},
	// tasks.Race joins every block's task, the first to end deciding.
	"TasksRace": {wait: childWait},
	// The with lifetime owner's scope (with, withClose; resources.md, Context
	// ownership). None waits, and none is a release route: the release a
	// with registers is the call of a checked Close, which masks itself
	// (callerRelease). A provider's release is registered only by
	// WithAcquire, in the masked step that builds it (resources.md R8), so
	// the catalog binds no bare registration (Scope.WithClose).
	"Scope.OpenWith":    {},
	"Scope.WithAcquire": {},
	"Scope.EndWith":     {},
	// The context owner (calls.go, contextWith; runtime.md, G1): a region
	// opens on the scope of the code at the with and ends at its block's
	// normal end, or when that scope closes. The reads take the running
	// task, whose immutable values a child shares from its start.
	"Scope.OpenContext": {},
	"Context.End":       {},
	"ContextBound":      {reads: true},
	"ContextValue":      {reads: true},
	// std strings and strconv (G5a; stdlib.md, strings and strconv): pure
	// bodies that neither wait nor release.
	"StringsSplit":      {},
	"StringsJoin":       {},
	"StringsHasPrefix":  {},
	"StringsHasSuffix":  {},
	"StringsTrimSpace":  {},
	"StringsTrimPrefix": {},
	"StringsTrimSuffix": {},
	"StringsFields":     {},
	"StringsRepeat":     {},
	"StringsToLower":    {},
	"StringsToUpper":    {},
	"StringsIndex":      {},
	"StringsContains":   {},
	"StringsSlice":      {},
	"StrconvItoa":       {},
	"StrconvAtoi":       {},
	"StrconvQuote":      {},
	// std-align S1c: the rest of Go's strings and strconv that the std
	// takes, and utf8; pure, as above.
	"StringsReplaceAll":   {},
	"StringsLastIndex":    {},
	"StringsCount":        {},
	"StringsSplitN":       {},
	"StringsEqualFold":    {},
	"StringsContainsRune": {},
	"UTF8RuneCount":       {},
	"StrconvParseInt":     {},
	"StrconvParseFloat":   {},
	"StrconvParseBool":    {},
	"StrconvFormatBool":   {},
	"StrconvFormatFloat":  {},
	"StrconvUnquote":      {},
	// std slices and cmp (std-align S2a; stdlib.md, Slices and Maps):
	// bodies that compute on their arguments and neither wait nor release.
	// SortFunc and SortStableFunc call the program's comparator, which has
	// an empty row (lowerer.adaptFunc), so they do not wait either.
	"SlicesSort":           {},
	"SlicesSortOwned":      {},
	"SlicesSortFunc":       {},
	"SlicesSortStableFunc": {},
	"SlicesContains":       {},
	"SlicesIndex":          {},
	"SlicesReverse":        {},
	"SlicesClone":          {},
	"SlicesConcat":         {},
	"SlicesRepeat":         {},
	"SlicesSlice":          {},
	"MapsClone":            {},
	"SlicesMax":            {},
	"SlicesMin":            {},
	"SlicesBinarySearch":   {},
	"SlicesEqual":          {},
	"CmpCompare":           {},
	"CmpLess":              {},
	// std time (std-align S2b; stdlib.md, time): TimeHostNow reads the
	// host clock and the others compute, so none waits; Timeline.Wait
	// waits on the caller's task (rt.Sleep on the manual timeline), and
	// Advance wakes sleepers without waiting.
	"TimeHostNow": {},
	"TimeFormat":  {},
	"TimeParse":   {},
	"TimeSub":     {},
	"TimeSubMono": {},
	// std-csv: pure bodies over encoding/csv on a string.
	"CSVReadAll": {},
	"CSVScan":    {},
	"CSVFormat":  {},
	// std machines (fs2): the defect of a timer that is not positive.
	"MachineTimerNotPositiveAt": {},
	// std-duration: pure bodies of duration text and arithmetic.
	"TimeParseDuration": {},
	"TimeDurationText":  {},
	"TimeTimes":         {},
	"TimeDivide":        {},
	"TimeCount":         {},
	"NewTimeline":       {},
	"Timeline.Read":     {},
	"Timeline.Wait":     {wait: callerWait},
	"Timeline.Advance":  {},
	"Timeline.Sleepers": {},
	// std testing (stdlib.md, Testing): NewVirtualTimeline makes the
	// calling task the first member of the clock's domain and returns; its
	// Wait is Timeline.Wait's.
	"NewVirtualTimeline": {wait: memberTask},
	// std rand (std-align S2c; stdlib.md, rand): each draw takes the
	// source's lock for one step of the PCG, or reads Go's global
	// generator, and none waits on a task.
	"NewStream":       {},
	"Stream.Uint64":   {},
	"Stream.IntN":     {},
	"Stream.Float64":  {},
	"RandHostUint64":  {},
	"RandHostIntN":    {},
	"RandHostFloat64": {},
	// std sync (stdlib.md, Sync and STM 1): each operation of a Ref holds
	// its mutex for one step, and an Update's or Modify's function has an
	// empty row, so none waits on a task and none releases.
	"NewRef":     {},
	"Ref.Get":    {},
	"Ref.Set":    {},
	"Ref.Update": {},
	"RefModify":  {},
	// std filepath (std-align S3a; stdlib.md, filepath): pure bodies that
	// neither wait nor release.
	"FilepathJoin":  {},
	"FilepathBase":  {},
	"FilepathDir":   {},
	"FilepathExt":   {},
	"FilepathClean": {},
	"FilepathIsAbs": {},
	"FilepathSplit": {},
	"FilepathRel":   {},
	// std utf8, strings, strconv, math and bits (std-text-runes;
	// stdlib.md, strings and strconv, math and bits): pure bodies that
	// neither wait nor release.
	"UTF8DecodeRuneInString": {},
	"UTF8RuneText":           {},
	"UTF8AppendRune":         {},
	"UTF8ValidString":        {},
	"UTF8ValidRune":          {},
	"UTF8RuneLen":            {},
	"StringsToValidUTF8":     {},
	"StringsCompare":         {},
	"StringsPadLeft":         {},
	"StringsPadRight":        {},
	"StrconvFormatInt":       {},
	"MathAbs":                {},
	"MathCeil":               {},
	"MathFloor":              {},
	"MathTrunc":              {},
	"MathRound":              {},
	"MathSqrt":               {},
	"MathPow":                {},
	"MathMod":                {},
	"MathInf":                {},
	"MathNaN":                {},
	"MathIsNaN":              {},
	"MathIsInf":              {},
	"BitsOnesCount":          {},
	"BitsOnesCount64":        {},
	"BitsLeadingZeros":       {},
	"BitsLeadingZeros64":     {},
	"BitsTrailingZeros":      {},
	"BitsTrailingZeros64":    {},
	"BitsLen":                {},
	"BitsLen64":              {},
	"BitsRotateLeft32":       {},
	"BitsRotateLeft64":       {},
	// std hex, base64, sha256 and crc32 (G3 std-digest; stdlib.md,
	// encodings and digests): pure bodies that neither wait nor release.
	"HexEncode":       {},
	"HexDecode":       {},
	"Base64StdEncode": {},
	"Base64URLEncode": {},
	"Base64StdDecode": {},
	"Base64URLDecode": {},
	"Sha256Sum":       {},
	"Crc32Checksum":   {},
	"Crc32Update":     {},
	// std os (G2; stdlib.md, host packages): bodies that read the host
	// process and neither wait nor release.
	"OSArgs":   {},
	"OSHasEnv": {},
	"OSGetenv": {},
	// std fs (G4, G4b; stdlib.md, fs): bodies that read or write the host
	// file system and neither wait nor release.
	"FSReadFile":  {},
	"FSReadDir":   {},
	"FSStat":      {},
	"FSWriteFile": {},
	"FSMkdirAll":  {},
	// fs writes with a mode, appends, renames and removes (std-fs-safe).
	"FSWriteFileMode": {},
	"FSMkdirAllMode":  {},
	"FSAppendFile":    {},
	"FSChmod":         {},
	"FSRename":        {},
	"FSRemove":        {},
	// std io (S1b; stdlib.md, io): bodies that write or read a standard
	// stream of the host process; none releases. A read of standard input
	// waits on the caller's task (batch-12 C3, decision O15): a stop of that
	// task ends the wait, and the line read after it stays for the next read.
	"IOPrint":      {},
	"IOEprint":     {},
	"IOIsTerminal": {},
	"IOReadAll":    {wait: callerWait},
	"IOLines":      {wait: callerWait},
	// std exec (std-align S3c; stdlib.md, exec): Run waits for the program
	// on the caller's task. Its cancel hook is the stop of that task: the
	// body kills the program's process group and waits for the reap
	// before it raises the interruption.
	"ExecRun": {wait: callerWait},
}

// runtimeCall binds a std body's hidden arguments at the point it runs.
// stdResult calls it inside the invocation environment for a deferred Seq.
// Wait uses the handle's owning task inside the primitive; Stop drains
// uninterruptibly. No extra poll is needed for any of these primitives.
func (fc *funcCtx) runtimeCall(name string, body v1.Body, typeArgs, recv string, vals []string, env string, x *syntax.CallExpr, c *check.Call, site syntax.Node) string {
	binding, ok := runtimeBindings[name]
	if !ok {
		ice(site, "runtime binding %s has no waiting policy", name)
	}
	if hasHidden(body.Hidden, v1.CallerTask) != binding.takesCaller() {
		ice(site, "runtime binding %s does not receive its declared waiting task", name)
	}
	if hasHidden(body.Hidden, v1.ReadingPackage) != binding.reads {
		ice(site, "runtime binding %s does not receive its declared context package", name)
	}
	if x != nil && binding.wait != noWait && fc.l.info.Closes[x] != nil {
		// A std Close is a release (R9). Today every std Close is atomic
		// (Queue.Close); a waiting one needs a release route first.
		ice(site, "the std Close binding %s waits, and no release route masks it", name)
	}
	var head string
	if recv == "" {
		head = fc.l.rt() + "." + name + typeArgs
	} else {
		i := strings.LastIndex(name, ".")
		if i < 0 || typeArgs != "" {
			ice(site, "runtime method binding %s has an invalid call shape", name)
		}
		head = "(" + recv + ")." + memberName(name[i+1:])
	}
	return head + "(" + strings.Join(append(fc.hiddenArgs(body.Hidden, x, c, env, site), vals...), ", ") + ")"
}

// hostBlockingIntrinsic adapts the catalog's finite host-provider bridges.
// Identity is the declared Go target, never the source function's name.
// The extern path has no host signature loader (D-E5); the trusted bridge
// records only its erased provider. BodyOf owns the Go signature, including
// its hidden task. A user extern is never a bridge: its call is a direct Go
// call (hostCall).
func (l *lowerer) hostBlockingIntrinsic(f *check.Func) func(*funcCtx, []string) string {
	target := strings.Trim(f.Decl.Extern.Target.Value, "\"`")
	for name, binding := range runtimeBindings {
		if binding.hostTarget == "" || target != binding.hostTarget {
			continue
		}
		body, err := v1.BodyOf(name)
		if err != nil {
			ice(f.Decl, "host bridge %s has no Go body: %v", target, err)
		}
		s := f.Sig
		if len(s.TParams) != 0 || len(s.Results) != 0 || len(body.Results) != 0 || len(s.Params) != len(body.Params)+1 || !isPreludeType(s.Params[0].Type, binding.provider) {
			return nil
		}
		for i, param := range body.Params {
			// A provider bridge currently admits duration operands. A new
			// Go operand shape fails closed until the host path owns it.
			if param.Type != "time.Duration" || !isBasic(s.Params[i+1].Type, check.Duration) {
				return nil
			}
		}
		return func(fc *funcCtx, params []string) string {
			return "\t" + fc.runtimeCall(name, body, "", "", params[1:], fc.envExpr(fc.site), nil, nil, fc.site) + "\n"
		}
	}
	return nil
}
