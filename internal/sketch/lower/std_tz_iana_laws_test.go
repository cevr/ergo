package lower

// Generated from the laws of internal/sketch/std/tz/iana by the TM lane's
// lawgen script; the probe restates each law as an Ergo test, and the
// statements are the ledger's (fu-ledger-runner).

// stdIanaLawsProbe restates the tz/iana laws whose runner is
// TestStdTzIanaLawsRun as Ergo tests over std tz/iana.
const stdIanaLawsProbe = `import tz
import tz/iana

test IanaEveryNameLoads(i int) {
	let names = iana.Names()
	let name = names[(if i < 0 { -(i + 1) } else { i }) % len(names)]
	let z = iana.Load(iana.New(), name) catch {
	case tz.UnknownZone:
		assert false
		return
	}
	assert tz.Name(z) == name && len(iana.Release()) >= 5
}

test IanaLoadRefusesOtherNames(name string) {
	for n in iana.Names() {
		if n == name {
			assert true
			return
		}
	}
	let _ = iana.Load(iana.New(), name) catch {
	case tz.UnknownZone as e:
		assert e.Name == name
		return
	}
	assert false
}
`

// stdIanaLawStatements is the statement of each law the probe
// restates, by ledger key; lawledger.Run holds it equal to the ledger.
var stdIanaLawStatements = map[string]string{
	"tz/iana EveryNameLoads":        `EveryNameLoads(i int) { let names = Names() let name = names[(if i < 0 { -(i + 1) } else { i }) % len(names)] let z = Load(New(), name) catch { case tz.UnknownZone: return false } tz.Name(z) == name && len(Release()) >= 5 }`,
	"tz/iana LoadRefusesOtherNames": `LoadRefusesOtherNames(name string) { for n in Names() { if n == name { return true } } let _ = Load(New(), name) catch { case tz.UnknownZone as e: return e.Name == name } false }`,
}
