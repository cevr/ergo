package lower

import "testing"

// TestRestartKeepsIntMapChoice is the smallest repro of an MI-2 bug fixed
// in a67ff3212: a lowering that bails on a flat union lowers the program
// again (lowerAgain, irState.restart), and restart rebuilt the irState
// without the round's int map choice, so the Map types an earlier round
// turned into Go maps were int maps again, and the last round's allGo was
// lost. The restarted state keeps the choice and drops only the abandoned
// run's conflicts (mutant mi2-restart-drops-choice).
func TestRestartKeepsIntMapChoice(t *testing.T) {
	st := &irState{maps: intMapState{
		off:       map[string]bool{"Map[int, int]": true},
		allGo:     true,
		conflicts: map[string]bool{"Map[int8, string]": true},
		why:       map[string]int{"body: x": 1},
	}}
	st.restart()
	if !st.maps.off["Map[int, int]"] || !st.maps.allGo {
		t.Fatalf("restart lost the round's int map choice: %+v", st.maps)
	}
	if len(st.maps.conflicts) != 0 || len(st.maps.why) != 0 {
		t.Fatalf("restart kept the abandoned run's conflicts: %+v", st.maps)
	}
}
