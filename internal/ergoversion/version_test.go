package ergoversion

import (
	"runtime/debug"
	"testing"
)

// TestOfReadsTheStamp (SPLIT E1): the version is the go command's stamp of
// the main module, else the pseudo-version of the VCS settings, else
// devel. Mutant (testdata/mutants.json): ev-vcs-fallback-dropped (a binary
// with a revision and no module version reads as devel).
func TestOfReadsTheStamp(t *testing.T) {
	vcs := func(modified string) []debug.BuildSetting {
		return []debug.BuildSetting{{Key: "vcs.revision", Value: "c501620891c0aa0bb0cc0dd0ee0ff00112233445"}, {Key: "vcs.time", Value: "2026-11-17T15:21:20Z"}, {Key: "vcs.modified", Value: modified}}
	}
	for _, c := range []struct {
		main     string
		settings []debug.BuildSetting
		want     string
	}{
		{"v1.2.3", nil, "v1.2.3"},
		{"v0.0.0-20261117152120-c501620891c0+dirty", nil, "v0.0.0-20261117152120-c501620891c0+dirty"},
		{"(devel)", vcs("false"), "v0.0.0-20261117152120-c501620891c0"},
		{"(devel)", vcs("true"), "v0.0.0-20261117152120-c501620891c0+dirty"},
		{"(devel)", nil, Devel},
		{"", []debug.BuildSetting{{Key: "vcs.revision", Value: "c501620891c0"}}, Devel},
	} {
		if got := Of(&debug.BuildInfo{Main: debug.Module{Version: c.main}, Settings: c.settings}); got != c.want {
			t.Errorf("Of(main %q, %v) = %q, want %q", c.main, c.settings, got, c.want)
		}
	}
}
