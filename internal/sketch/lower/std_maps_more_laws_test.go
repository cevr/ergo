package lower

// Generated from the laws of internal/sketch/std/maps by the TM lane's
// lawgen script; the probe restates each law as an Ergo test, and the
// statements are the ledger's (fu-ledger-runner).

// stdMapsMoreLawsProbe restates the maps laws whose runner is
// TestStdMapsMoreLawsRun as Ergo tests over std maps.
const stdMapsMoreLawsProbe = `import maps

test MapsMergeRightWins(a Map[string, int], b Map[string, int]) {
	let m = maps.Merge(a, b)
	let mut ok = true
	for k, v in a {
		ok = ok && m.Get(k) == (if b.Get(k) == None { Some[int]{Value: v} } else { b.Get(k) })
	}
	for k, v in b {
		ok = ok && m.Get(k) == Some[int]{Value: v}
	}
	for k, _ in m {
		ok = ok && (a.Get(k) != None || b.Get(k) != None)
	}
	assert ok
}

test MapsFilterAndTransformKeepKeys(m Map[string, int]) {
	let kept = maps.Filter(m, func(k string, v int) bool { v%2 == 0 })
	let doubled = maps.TransformValues(m, func(v int) int { v * 2 })
	let mut ok = len(doubled) == len(m)
	for k, v in m {
		ok = ok && kept.Get(k) == (if v%2 == 0 { Some[int]{Value: v} } else { None }) && doubled.Get(k) == Some[int]{Value: v * 2}
	}
	assert ok
}

test MapsEntriesAreSorted(m Map[string, int]) {
	let es = maps.Entries(m)
	let mut ok = len(es) == len(m)
	for i, e in es {
		let (k, v) = e
		ok = ok && m.Get(k) == Some[int]{Value: v}
		if i > 0 {
			let (before, _) = es[i-1]
			ok = ok && before < k
		}
	}
	assert ok
}
`

// stdMapsMoreLawStatements is the statement of each law the probe
// restates, by ledger key; lawledger.Run holds it equal to the ledger.
var stdMapsMoreLawStatements = map[string]string{
	"maps MergeRightWins":             `MergeRightWins(a Map[string, int], b Map[string, int]) { let m = Merge(a, b) let mut ok = true for k, v in a { ok = ok && m.Get(k) == (if b.Get(k) == None { Some[int]{Value: v} } else { b.Get(k) }) } for k, v in b { ok = ok && m.Get(k) == Some[int]{Value: v} } for k, _ in m { ok = ok && (a.Get(k) != None || b.Get(k) != None) } ok }`,
	"maps FilterAndTransformKeepKeys": `FilterAndTransformKeepKeys(m Map[string, int]) { let kept = Filter(m, func(k string, v int) bool { v%2 == 0 }) let doubled = TransformValues(m, func(v int) int { v * 2 }) let mut ok = len(doubled) == len(m) for k, v in m { ok = ok && kept.Get(k) == (if v%2 == 0 { Some[int]{Value: v} } else { None }) && doubled.Get(k) == Some[int]{Value: v * 2} } ok }`,
	"maps EntriesAreSorted":           `EntriesAreSorted(m Map[string, int]) { let es = Entries(m) let mut ok = len(es) == len(m) for i, e in es { let (k, v) = e ok = ok && m.Get(k) == Some[int]{Value: v} if i > 0 { let (before, _) = es[i-1] ok = ok && before < k } } ok }`,
}
