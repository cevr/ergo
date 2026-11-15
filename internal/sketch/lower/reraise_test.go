package lower

import "testing"

// TestReraiseWiredRow is the smallest repro of the re-raise bug (wf10
// corpus wave, lane ST, reraise.ergo): `case err: return err` in a catch
// of decode[T], in a function that returns T, lowered `return err_1` as
// the success value, because the lowering did not read wired[T] as a row
// of error types, so the lowered Go did not type-check. One function's
// row is wired[T] alone, the other's mixes it with a declared error type.
//
// A caught row is a union even with one member, so both shapes reach
// the same rule. Mutant (lower ledger): reraise-wired-member (a wired[T]
// member of a row is not an error value) fails both functions.
func TestReraiseWiredRow(t *testing.T) {
	const src = `import codec

type bad { Offset int }

func check(n int) fails bad {
	if n > 0 {
		return bad{Offset: n}
	}
}

func only[T wire](r Reader) T fails wired[T] {
	let v = decode[T](r) catch {
	case err:
		return err
	}
	v
}

func mixed[T wire](r Reader, n int) T fails bad | wired[T] {
	let v = decode[T](r) catch {
	case err:
		try check(n)
		return err
	}
	v
}

func main() needs Console {
	let a = only[int](codec.NewReplay([]codec.Event{codec.StringEvent{V: "a"}})) catch {
	case _:
		-1
	}
	let b = mixed[int](codec.NewReplay([]codec.Event{codec.StringEvent{V: "a"}}), 0) catch {
	case _:
		-1
	}
	if a == b {
		Console.Println("same")
	}
}
`
	typecheckGo(t, []*lowered{lowerTestBuild(t, "reraise", src)})
}
