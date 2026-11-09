// MU7 machine-supervise (runtime/v1 actor.go, a supervised runner): one
// actor whose runner restarts after a defect within a budget of LIMIT
// restarts (tasks.Restart{Limit}). Senders admit asks and sends into the
// inbox while admission is open; the runner steps the head (ok, Done, or a
// defect), and a work's defect may also arrive between steps. A crash ends
// the incarnation as an end does (runner rule 8) but keeps admission open:
// the ask in the step and every queued ask are answered Stopped, queued
// sends are dropped, and the new incarnation starts from a fresh state.
// Stop requests the runner's end and closes admission; Drain closes
// admission and lets the queue run out. Done, Stop and Drain are
// terminal: no restart after them. When the budget is spent, the actor
// ends with the defect.
//
// The abstract state `st` counts the commits the actor has made since it
// started from a fresh state; the ghost `commits` counts the commits of
// the current incarnation, so st == commits says no restart reused the
// in-memory state.
//
// Invariants (`safe`):
//   - withinBudget: no more than LIMIT restarts;
//   - freshState: st == commits (a restart never reuses the state);
//   - asksAnswered: every admitted ask is queued or answered, once;
//   - endAnswered: when the actor has ended, every admitted ask is answered;
//   - terminalFinal: after an end, no new incarnation runs;
//   - exhaustion: an end by a defect comes with the budget spent, or after
//     a Stop or a Drain closed admission.
// MUTANT selects a broken variant, each of which must break `safe`:
//   1: a restart keeps the last state (reuse-state)
//   2: a crash drops the queued asks unanswered (ask-unanswered)
//   3: a work's defect after Done restarts the actor (restart-after-done)
//   4: one restart past the budget (budget-off-by-one)
module supervise {
  const MUTANT: int

  pure val LIMIT = 2
  pure val MAXID = 5

  // Envelopes: an id and whether it is an ask.
  var queue: List[{ id: int, ask: bool }]
  var nextId: int
  var admitted: Set[int]      // ask ids
  var answers: int -> int     // ask id -> times answered
  var st: int
  var commits: int
  var inc: int
  var restarts: int
  var running: bool
  var closed: bool            // admission closed (Stop or Drain)
  var stopReq: bool
  var exit: str               // "", "done", "halted", "defect"
  var everEnded: bool
  var incAtEnd: int

  action init = all {
    queue' = List(),
    nextId' = 0,
    admitted' = Set(),
    answers' = Map(),
    st' = 0,
    commits' = 0,
    inc' = 0,
    restarts' = 0,
    running' = true,
    closed' = false,
    stopReq' = false,
    exit' = "",
    everEnded' = false,
    incAtEnd' = 0,
  }

  def answer(m: int -> int, ids: Set[int]): int -> int =
    ids.fold(m, (acc, i) => acc.put(i, if (acc.keys().contains(i)) acc.get(i) + 1 else 1))

  def queuedAsks(q: List[{ id: int, ask: bool }]): Set[int] =
    q.indices().filter(i => q[i].ask).map(i => q[i].id)

  def queuedIds(q: List[{ id: int, ask: bool }]): Set[int] =
    q.indices().map(i => q[i].id)

  // A sender admits an ask or a send.
  action admit(isAsk: bool): bool = all {
    running, not(closed), nextId < MAXID,
    queue' = queue.append({ id: nextId, ask: isAsk }),
    nextId' = nextId + 1,
    admitted' = if (isAsk) admitted.union(Set(nextId)) else admitted,
    answers' = answers,
    st' = st, commits' = commits, inc' = inc, restarts' = restarts,
    running' = running, closed' = closed, stopReq' = stopReq, exit' = exit,
    everEnded' = everEnded, incAtEnd' = incAtEnd,
  }

  // The runner steps the head: it commits and answers its ask.
  action stepOk = all {
    running, not(stopReq), queue.length() > 0,
    val h = queue.head()
    all {
      queue' = queue.tail(),
      answers' = if (h.ask) answer(answers, Set(h.id)) else answers,
    },
    st' = st + 1, commits' = commits + 1,
    nextId' = nextId, admitted' = admitted, inc' = inc, restarts' = restarts,
    running' = running, closed' = closed, stopReq' = stopReq, exit' = exit,
    everEnded' = everEnded, incAtEnd' = incAtEnd,
  }

  // The end of the actor (rule 8): queued asks answered Stopped.
  def ended(q: List[{ id: int, ask: bool }], ans: int -> int): int -> int =
    answer(ans, queuedAsks(q))

  // The head's step decides Done: answered, then the actor ends.
  action stepDone = all {
    running, not(stopReq), queue.length() > 0,
    val h = queue.head()
    val ans = if (h.ask) answer(answers, Set(h.id)) else answers
    all {
      queue' = List(),
      answers' = ended(queue.tail(), ans),
    },
    running' = false, exit' = "done", everEnded' = true, incAtEnd' = inc,
    st' = st, commits' = commits,
    nextId' = nextId, admitted' = admitted, inc' = inc, restarts' = restarts,
    closed' = true, stopReq' = stopReq,
  }

  // A crash: the head's step (inStep) or a work's defect between steps.
  // The ask in the step and the queued asks are answered Stopped; then a
  // restart, or the end with the defect.
  action crash(inStep: bool): bool = all {
    running, not(stopReq),
    (not(inStep) or queue.length() > 0),
    val cur = if (inStep and queue.head().ask) Set(queue.head().id) else Set()
    val rest = if (inStep) queue.tail() else queue
    val ans = answer(answers, cur.union(if (MUTANT == 2) Set() else queuedAsks(rest)))
    val again = restarts < LIMIT + (if (MUTANT == 4) 1 else 0) and not(closed)
    all {
      queue' = List(),
      answers' = ans,
      restarts' = if (again) restarts + 1 else restarts,
      inc' = if (again) inc + 1 else inc,
      st' = if (again and MUTANT != 1) 0 else st,
      commits' = if (again) 0 else commits,
      running' = again,
      exit' = if (again) exit else "defect",
      everEnded' = (everEnded or not(again)),
      incAtEnd' = if (again) incAtEnd else inc,
    },
    nextId' = nextId, admitted' = admitted,
    closed' = closed, stopReq' = stopReq,
  }

  // Stop: the request closes admission; the runner ends at its next take.
  action stop = all {
    running, not(stopReq),
    stopReq' = true, closed' = true,
    queue' = queue, nextId' = nextId, admitted' = admitted, answers' = answers,
    st' = st, commits' = commits, inc' = inc, restarts' = restarts,
    running' = running, exit' = exit, everEnded' = everEnded, incAtEnd' = incAtEnd,
  }

  action stopEnds = all {
    running, stopReq,
    queue' = List(),
    answers' = ended(queue, answers),
    running' = false, exit' = "halted", everEnded' = true, incAtEnd' = inc,
    nextId' = nextId, admitted' = admitted, st' = st, commits' = commits,
    inc' = inc, restarts' = restarts, closed' = closed, stopReq' = stopReq,
  }

  // Drain closes admission; the runner ends once the queue ran out.
  action drain = all {
    running, not(closed),
    closed' = true,
    queue' = queue, nextId' = nextId, admitted' = admitted, answers' = answers,
    st' = st, commits' = commits, inc' = inc, restarts' = restarts,
    running' = running, stopReq' = stopReq, exit' = exit,
    everEnded' = everEnded, incAtEnd' = incAtEnd,
  }

  action drained = all {
    running, closed, not(stopReq), queue.length() == 0,
    running' = false, exit' = "halted", everEnded' = true, incAtEnd' = inc,
    queue' = queue, nextId' = nextId, admitted' = admitted, answers' = answers,
    st' = st, commits' = commits, inc' = inc, restarts' = restarts,
    closed' = closed, stopReq' = stopReq,
  }

  // MUTANT 3: a work's defect reported after Done restarts the actor.
  action lateDefect = all {
    MUTANT == 3, not(running), exit == "done", restarts < LIMIT,
    running' = true, inc' = inc + 1, restarts' = restarts + 1,
    st' = 0, commits' = 0, closed' = false,
    queue' = queue, nextId' = nextId, admitted' = admitted, answers' = answers,
    stopReq' = stopReq, exit' = exit, everEnded' = everEnded, incAtEnd' = incAtEnd,
  }

  action step = any {
    nondet isAsk = Set(true, false).oneOf()
    admit(isAsk),
    stepOk,
    stepDone,
    nondet inStep = Set(true, false).oneOf()
    crash(inStep),
    stop,
    stopEnds,
    drain,
    drained,
    lateDefect,
  }

  val withinBudget = restarts <= LIMIT
  val freshState = st == commits
  val asksAnswered = admitted.forall(i =>
    if (queuedIds(queue).contains(i)) not(answers.keys().contains(i))
    else answers.keys().contains(i) and answers.get(i) == 1)
  val endAnswered = not(running) implies admitted.forall(i => answers.keys().contains(i))
  val terminalFinal = everEnded implies (not(running) and inc == incAtEnd)
  val exhaustion = exit == "defect" implies (restarts == LIMIT or closed)
  val safe = withinBudget and freshState and asksAnswered and endAnswered and terminalFinal and exhaustion

  // Witnesses: the design must break each (the runs reach the case).
  val noRestart = restarts == 0
  val noExhaustion = not(exit == "defect" and not(closed))
  val noFreshAfterCommits = not(restarts > 0 and st == 0 and admitted.size() > 0)
}

module design { import supervise(MUTANT = 0).* }
module reuseState { import supervise(MUTANT = 1).* }
module askUnanswered { import supervise(MUTANT = 2).* }
module restartAfterDone { import supervise(MUTANT = 3).* }
module budgetOffByOne { import supervise(MUTANT = 4).* }
