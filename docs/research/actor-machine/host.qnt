// mu-host (fu-mb F12, em-parity M63): an actor's host, std actor.Host[A],
// as a Quint model. A step makes empty hosts and puts them in the state
// it enters; the runner binds each one to that entry. The entry's work
// fills a host with a child (actor.ChildIn); a host is filled once, only
// by the work of the entry it is bound to, and never after that entry
// ended. The runner publishes the entry only once its work has filled the
// entry's hosts, or has reached its first wait, or has ended. A reader
// takes a host from a published state and calls Get: the child when the
// host is filled, Stopped once its entry ended, and otherwise it waits.
// A state may carry an old host into a new entry (the program copies the
// field); that host stays bound to its old entry, so it is closed.
//
// Invariants (`safe`):
//   - FillBeforePublish: once the current entry is published, each of its
//     hosts is filled, or its work has waited or ended (a reader that sees
//     the entry waits for a fill only when the work itself waited first);
//   - NoFillAfterEnd: no host is filled after its entry ended, or by the
//     work of another entry;
//   - GetSeesItsEntry: a Get gives only a child of the entry the host was
//     bound to when the reader took it.
//
// MUTANT selects a broken variant; each must break the invariant named:
//   1 publishEarly:  the runner publishes as soon as the entry is
//                    committed (FillBeforePublish)
//   2 fillAfterEnd:  ChildIn fills any empty host the state holds, with no
//                    entry check (NoFillAfterEnd)
//   3 getNewer:      a carried host is bound again to the new entry, so the
//                    new work fills it and an old reader's Get gives a child
//                    of a newer entry (GetSeesItsEntry)
module host {
  const MUTANT: int

  pure val HOSTS = Set(0, 1, 2, 3)
  pure val MAXE = 4

  type View = { h: int, owner: int }

  var epoch: int
  var published: int
  // workPhase is "run" (before its first wait), "waited" or "ended".
  var workPhase: str
  // hostEntry is the entry a host is bound to, -1 before it is made.
  var hostEntry: int -> int
  var filled: int -> bool
  var childOf: int -> int
  var closed: int -> bool
  // inState: the hosts the current state holds.
  var inState: Set[int]
  var views: List[View]
  var lateFill: bool
  var badGet: bool
  // Counts for the witnesses.
  var stoppedGets: int
  var childGets: int
  var waitPublishes: int
  var carried: int

  action init = all {
    epoch' = 0, published' = 0, workPhase' = "ended",
    hostEntry' = HOSTS.mapBy(h => -1), filled' = HOSTS.mapBy(h => false),
    childOf' = HOSTS.mapBy(h => -1), closed' = HOSTS.mapBy(h => false),
    inState' = Set(), views' = List(), lateFill' = false, badGet' = false,
    stoppedGets' = 0, childGets' = 0, waitPublishes' = 0, carried' = 0,
  }

  // enter: the step enters a new state with the fresh hosts fresh and the
  // carried hosts carry. The old entry ends: its hosts close. The runner
  // takes no signal before it published the entry it is in.
  action enter(fresh: Set[int], carry: Set[int]): bool = {
    val e = epoch + 1
    val ended = HOSTS.filter(h => hostEntry.get(h) == epoch)
    val rebound = if (MUTANT == 3) carry else Set()
    all {
      published == epoch, epoch < MAXE,
      fresh.forall(h => hostEntry.get(h) == -1), fresh.size() <= 2,
      carry.subseteq(inState),
      epoch' = e,
      workPhase' = "run",
      hostEntry' = HOSTS.mapBy(h => if (fresh.contains(h) or rebound.contains(h)) e else hostEntry.get(h)),
      closed' = HOSTS.mapBy(h => if (rebound.contains(h)) false
        else if (ended.contains(h)) true else closed.get(h)),
      filled' = HOSTS.mapBy(h => if (rebound.contains(h)) false else filled.get(h)),
      inState' = fresh.union(carry),
      carried' = carried + carry.size(),
      published' = published, childOf' = childOf, views' = views,
      lateFill' = lateFill, badGet' = badGet,
      stoppedGets' = stoppedGets, childGets' = childGets, waitPublishes' = waitPublishes,
    }
  }

  // fill: the current entry's work fills h with a child (ChildIn).
  action fill(h: int): bool = all {
    workPhase != "ended", inState.contains(h), not(filled.get(h)),
    if (MUTANT == 2) true else (hostEntry.get(h) == epoch and not(closed.get(h))),
    filled' = filled.set(h, true),
    childOf' = childOf.set(h, epoch),
    lateFill' = (lateFill or closed.get(h) or hostEntry.get(h) != epoch),
    epoch' = epoch, published' = published, workPhase' = workPhase,
    hostEntry' = hostEntry, closed' = closed, inState' = inState, views' = views,
    badGet' = badGet, stoppedGets' = stoppedGets, childGets' = childGets,
    waitPublishes' = waitPublishes, carried' = carried,
  }

  action workMoves(phase: str): bool = all {
    (workPhase == "run" and phase == "waited") or (workPhase != "ended" and phase == "ended"),
    workPhase' = phase,
    epoch' = epoch, published' = published, hostEntry' = hostEntry, filled' = filled,
    childOf' = childOf, closed' = closed, inState' = inState, views' = views,
    lateFill' = lateFill, badGet' = badGet, stoppedGets' = stoppedGets,
    childGets' = childGets, waitPublishes' = waitPublishes, carried' = carried,
  }

  def entryFilled = HOSTS.filter(h => inState.contains(h) and hostEntry.get(h) == epoch)
    .forall(h => filled.get(h))

  // publish: the runner shows the entry to observers.
  action publish = all {
    published < epoch,
    MUTANT == 1 or entryFilled or workPhase != "run",
    published' = epoch,
    waitPublishes' = if (entryFilled) waitPublishes else waitPublishes + 1,
    epoch' = epoch, workPhase' = workPhase, hostEntry' = hostEntry, filled' = filled,
    childOf' = childOf, closed' = closed, inState' = inState, views' = views,
    lateFill' = lateFill, badGet' = badGet, stoppedGets' = stoppedGets,
    childGets' = childGets, carried' = carried,
  }

  // take: a reader reads the published state and keeps one of its hosts.
  action take(h: int): bool = all {
    published == epoch, inState.contains(h), views.length() < 4,
    views' = views.append({ h: h, owner: hostEntry.get(h) }),
    epoch' = epoch, published' = published, workPhase' = workPhase,
    hostEntry' = hostEntry, filled' = filled, childOf' = childOf, closed' = closed,
    inState' = inState, lateFill' = lateFill, badGet' = badGet,
    stoppedGets' = stoppedGets, childGets' = childGets,
    waitPublishes' = waitPublishes, carried' = carried,
  }

  // get: a reader's Get on the host it took returns (a Get on an empty
  // open host waits: no step).
  action read(i: int): bool = all {
    i < views.length(),
    getAt(views[i]),
    epoch' = epoch, published' = published, workPhase' = workPhase,
    hostEntry' = hostEntry, filled' = filled, childOf' = childOf, closed' = closed,
    inState' = inState, views' = views, lateFill' = lateFill,
    waitPublishes' = waitPublishes, carried' = carried,
  }

  action getAt(v: View): bool = any {
    all {
      closed.get(v.h),
      stoppedGets' = stoppedGets + 1, childGets' = childGets, badGet' = badGet,
    },
    all {
      not(closed.get(v.h)), filled.get(v.h),
      childGets' = childGets + 1, stoppedGets' = stoppedGets,
      badGet' = (badGet or childOf.get(v.h) != v.owner),
    },
  }

  action step = {
    nondet fresh = HOSTS.powerset().oneOf()
    nondet carry = HOSTS.powerset().oneOf()
    nondet h = HOSTS.oneOf()
    nondet i = Set(0, 1, 2, 3).oneOf()
    nondet phase = Set("waited", "ended").oneOf()
    any { enter(fresh, carry), fill(h), workMoves(phase), publish, take(h), read(i) }
  }

  val FillBeforePublish = published == epoch implies (entryFilled or workPhase != "run")
  val NoFillAfterEnd = not(lateFill)
  val GetSeesItsEntry = not(badGet)
  val safe = FillBeforePublish and NoFillAfterEnd and GetSeesItsEntry

  // Witnesses: each `no…` must be violated by the design, so `safe` is
  // not vacuous: a Get gives a child; a Get fails Stopped; an entry is
  // published with an empty host because its work waited first; a state
  // carries a host into a new entry.
  val noChildGet = childGets == 0
  val noStoppedGet = stoppedGets == 0
  val noWaitPublish = waitPublishes == 0
  val noCarry = carried == 0
}

module design { import host(MUTANT = 0).* }
module publishEarly { import host(MUTANT = 1).* }
module fillAfterEnd { import host(MUTANT = 2).* }
module getNewer { import host(MUTANT = 3).* }
