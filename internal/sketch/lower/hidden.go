package lower

import v1 "ergo.local/prototype/runtime/v1"

// hiddenKinds builds the hidden arguments of a body contract (v1.Hidden),
// one method per kind: the emitter's Go text (emitHidden) and the IR
// builder's nodes (irHidden) are its two implementations, so a kind one
// of them does not build does not compile.
type hiddenKinds[T any] interface {
	frame() T
	callSite() T
	callerTask() T
	callerScheduler() T
	graph() T
	readingPackage() T
	resultRelease() T
	unknown(h v1.Hidden) T
}

// hiddenArg is what w builds for the hidden kind h. It is the one place
// the lowering reads the runtime's hidden-argument kinds, for the emitter
// (funcCtx.hiddenArgs) and the IR builder (irBuilder.hidden) alike; a
// declaration any module can write in their place is PK-NP's debt.
func hiddenArg[T any](h v1.Hidden, w hiddenKinds[T]) T {
	switch h {
	case v1.Frame:
		return w.frame()
	case v1.CallSite:
		return w.callSite()
	case v1.CallerTask:
		return w.callerTask()
	case v1.CallerScheduler:
		return w.callerScheduler()
	case v1.Graph:
		return w.graph()
	case v1.ReadingPackage:
		return w.readingPackage()
	case v1.ResultRelease:
		return w.resultRelease()
	}
	return w.unknown(h)
}
