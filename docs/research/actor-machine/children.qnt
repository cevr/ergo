// MU5 machine-children: the children of a machine actor's entry (std
// actor.Child, runtime/v1 actor.go rule 14), as a Quint model. Node 0 is a
// root actor; nodes 1.. are machine actors that a work of some live
// actor's entry starts as its children, down to depth DEPTH. A child is
// owned by its parent's entry (owner, ownerEpoch): it is kept in that
// entry's list, in start order, by identity. A child's output (an Emit or
// its Done) is pushed into its parent's inbox tagged with the parent's
// entry; the parent steps the front of its inbox while no exit runs and
// drops an output of an ended entry. When an entry ends (a re-entry) or
// the actor ends, the actor stops its children one at a time, from the
// last started to the first, each one stopping its own children first,
// and only then opens the next entry or ends. A child may also end by
// itself (Done); it stops its own children first, too.
//
// Invariants (`safe`):
//   - ChildInsideEntry: a live child's parent is live and still in the
//     entry that started the child;
//   - NoLateChildOutput: no output of an ended entry is stepped;
//   - ChildrenStopFirst: no live child has an ended parent, and a child
//     is stopped only when every sibling started after it has ended
//     (reverse start order).
//
// MUTANT selects a broken variant; each must break the invariant named:
//   1 lateOutput:  the parent steps a child output without its entry check
//                  (NoLateChildOutput)
//   2 parentFirst: an actor ends or opens its next entry without waiting
//                  for its children (ChildrenStopFirst)
//   3 byKind:      an entry keeps its children by kind: a second child of
//                  a kind replaces the first in the list, which is then
//                  never stopped (ChildInsideEntry)
module children {
  const MUTANT: int

  pure val NODES = Set(0, 1, 2, 3, 4)
  pure val DEPTH = 3
  pure val CAP = 2
  // Two kinds of child: two editors (1, 2) collide when kept by kind.
  pure val KIND = Map(0 -> "root", 1 -> "editor", 2 -> "editor", 3 -> "list", 4 -> "editor")

  type Out = { from: int, epoch: int }

  var alive: int -> bool
  var used: Set[int]
  var owner: int -> int
  var ownerEpoch: int -> int
  var epoch: int -> int
  var depth: int -> int
  var kids: int -> List[int]
  // closing is "" (an entry is open), "reenter" (the entry's exit runs)
  // or "end" (the actor's end runs).
  var closing: int -> str
  // stopReq: the node's parent asked it to stop.
  var stopReq: int -> bool
  var inbox: int -> List[Out]
  var late: bool
  var orderBad: bool
  // Counts for the witnesses.
  var dropped: int
  var kidStops: int
  var reentries: int

  action init = all {
    alive' = NODES.mapBy(n => n == 0),
    used' = Set(0),
    owner' = NODES.mapBy(n => -1),
    ownerEpoch' = NODES.mapBy(n => -1),
    epoch' = NODES.mapBy(n => 0),
    depth' = NODES.mapBy(n => 0),
    kids' = NODES.mapBy(n => List()),
    closing' = NODES.mapBy(n => ""),
    stopReq' = NODES.mapBy(n => false),
    inbox' = NODES.mapBy(n => List()),
    late' = false,
    orderBad' = false,
    dropped' = 0,
    kidStops' = 0,
    reentries' = 0,
  }

  action keep = all {
    late' = late, orderBad' = orderBad, dropped' = dropped, kidStops' = kidStops,
    reentries' = reentries,
  }

  // A work of p's open entry starts c as a child of that entry.
  action start(p: int, c: int): bool = all {
    alive.get(p), closing.get(p) == "", not(used.contains(c)), depth.get(p) < DEPTH,
    alive' = alive.set(c, true),
    used' = used.union(Set(c)),
    owner' = owner.set(c, p),
    ownerEpoch' = ownerEpoch.set(c, epoch.get(p)),
    depth' = depth.set(c, depth.get(p) + 1),
    kids' = kids.set(p,
      if (MUTANT == 3) kids.get(p).select(k => KIND.get(k) != KIND.get(c)).append(c)
      else kids.get(p).append(c)),
    epoch' = epoch, closing' = closing, stopReq' = stopReq, inbox' = inbox,
    keep,
  }

  // A live child gives an output (an Emit, or its Done before it ends);
  // it goes to its parent's inbox with the parent's entry, while the
  // parent is live (its inbox is closed after) and has room.
  action emit(c: int): bool = all {
    c != 0, alive.get(c), alive.get(owner.get(c)), inbox.get(owner.get(c)).length() < CAP,
    inbox' = inbox.set(owner.get(c),
      inbox.get(owner.get(c)).append({ from: c, epoch: ownerEpoch.get(c) })),
    alive' = alive, used' = used, owner' = owner, ownerEpoch' = ownerEpoch, epoch' = epoch,
    depth' = depth, kids' = kids, closing' = closing, stopReq' = stopReq,
    keep,
  }

  // p's runner takes the front of its inbox while no exit runs: an output
  // of the open entry is stepped, one of an ended entry is dropped.
  action take(p: int): bool = all {
    alive.get(p), closing.get(p) == "", inbox.get(p).length() > 0,
    val m = inbox.get(p).head()
    val stale = m.epoch != epoch.get(p)
    all {
      inbox' = inbox.set(p, inbox.get(p).tail()),
      late' = (late or (stale and MUTANT == 1)),
      dropped' = if (stale and MUTANT != 1) dropped + 1 else dropped,
      orderBad' = orderBad, kidStops' = kidStops, reentries' = reentries,
    },
    alive' = alive, used' = used, owner' = owner, ownerEpoch' = ownerEpoch, epoch' = epoch,
    depth' = depth, kids' = kids, closing' = closing, stopReq' = stopReq,
  }

  // p's step re-enters (an Enter or an Emit): the entry's exit begins.
  action reenter(p: int): bool = all {
    alive.get(p), closing.get(p) == "",
    closing' = closing.set(p, "reenter"),
    alive' = alive, used' = used, owner' = owner, ownerEpoch' = ownerEpoch, epoch' = epoch,
    depth' = depth, kids' = kids, stopReq' = stopReq, inbox' = inbox,
    keep,
  }

  // The root is stopped, or a child ends by itself (Done): its end begins.
  action selfEnd(p: int): bool = all {
    alive.get(p), closing.get(p) != "end",
    closing' = closing.set(p, "end"),
    alive' = alive, used' = used, owner' = owner, ownerEpoch' = ownerEpoch, epoch' = epoch,
    depth' = depth, kids' = kids, stopReq' = stopReq, inbox' = inbox,
    keep,
  }

  // p's exit stops its last started child that is not stopping yet, and
  // waits for its end before the next (endNode removes it).
  action stopNext(p: int): bool = all {
    alive.get(p), closing.get(p) != "", kids.get(p).length() > 0,
    val c = kids.get(p)[kids.get(p).length() - 1]
    all {
      not(stopReq.get(c)),
      stopReq' = stopReq.set(c, true),
      closing' = closing.set(c, "end"),
    },
    alive' = alive, used' = used, owner' = owner, ownerEpoch' = ownerEpoch, epoch' = epoch,
    depth' = depth, kids' = kids, inbox' = inbox,
    keep,
  }

  // n ends once its children have ended; it leaves its parent's list and
  // its inbox closes.
  action endNode(n: int): bool = all {
    alive.get(n), closing.get(n) == "end", MUTANT == 2 or kids.get(n).length() == 0,
    val p = owner.get(n)
    val sibs = if (p >= 0) kids.get(p) else List()
    all {
      alive' = alive.set(n, false),
      kids' = if (p >= 0) kids.set(p, sibs.select(k => k != n)) else kids,
      orderBad' = (orderBad or (stopReq.get(n) and sibs.length() > 0 and sibs[sibs.length() - 1] != n)),
      kidStops' = if (stopReq.get(n)) kidStops + 1 else kidStops,
    },
    inbox' = inbox.set(n, List()),
    used' = used, owner' = owner, ownerEpoch' = ownerEpoch, epoch' = epoch,
    depth' = depth, closing' = closing, stopReq' = stopReq,
    late' = late, dropped' = dropped, reentries' = reentries,
  }

  // p opens its next entry once the old entry's children have ended.
  action open(p: int): bool = all {
    alive.get(p), closing.get(p) == "reenter", MUTANT == 2 or kids.get(p).length() == 0,
    epoch' = epoch.set(p, epoch.get(p) + 1),
    closing' = closing.set(p, ""),
    kids' = kids.set(p, List()),
    reentries' = reentries + 1,
    alive' = alive, used' = used, owner' = owner, ownerEpoch' = ownerEpoch,
    depth' = depth, stopReq' = stopReq, inbox' = inbox,
    late' = late, orderBad' = orderBad, dropped' = dropped, kidStops' = kidStops,
  }

  action step = {
    nondet p = NODES.oneOf()
    nondet c = NODES.oneOf()
    any {
      start(p, c), emit(p), take(p), reenter(p), selfEnd(p), stopNext(p), endNode(p), open(p),
    }
  }

  val ChildInsideEntry = NODES.forall(c => (c != 0 and alive.get(c) and alive.get(owner.get(c)))
    implies ownerEpoch.get(c) == epoch.get(owner.get(c)))
  val NoLateChildOutput = not(late)
  val ChildrenStopFirst = not(orderBad) and NODES.forall(c => (c != 0 and alive.get(c))
    implies alive.get(owner.get(c)))
  val safe = ChildInsideEntry and NoLateChildOutput and ChildrenStopFirst

  // Witnesses: each `no…` must be violated by the design, so `safe` is
  // not vacuous: a stale output is dropped; an entry re-opens after its
  // children stopped; two editors live under one entry; a child at depth
  // DEPTH lives; the root ends after stopping two children.
  val noDrop = dropped == 0
  val noReentryAfterStops = not(reentries > 0 and kidStops > 0)
  val noTwoEditors = not(NODES.exists(p => alive.get(p) and
    kids.get(p).select(k => KIND.get(k) == "editor" and alive.get(k)).length() >= 2))
  val noDeepChild = not(NODES.exists(n => alive.get(n) and depth.get(n) == DEPTH))
  val noRootEndAfterStops = not(not(alive.get(0)) and kidStops >= 2)
}

module design { import children(MUTANT = 0).* }
module lateOutput { import children(MUTANT = 1).* }
module parentFirst { import children(MUTANT = 2).* }
module byKind { import children(MUTANT = 3).* }
