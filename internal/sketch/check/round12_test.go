package check_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestRowVariableWitnesses: the round-11 review's witnesses (class F).
// cache-witness.ergo: initializer discovery builds Box{f} before f's row is
// inferred; Box[func()]{f} must still be refused (S108), since f fails with
// Bang. row-gap-witness.ergo: a row built during discovery, error[Fault[f's
// type]], must not make a valid later instantiation wider than itself.
func TestRowVariableWitnesses(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("testdata", "classf", "*.ergo"))
	if len(files) < 67 {
		t.Fatalf("want the 44 witnesses and controls of rounds 12 to 16, found %v", files)
	}
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			_, diags := check.CheckSource(string(data))
			want, have := expected(string(data)), got(diags)
			if strings.Join(want, " ") != strings.Join(have, " ") {
				t.Errorf("want %v, got %v\n\t%s", want, have, describe(diags))
			}
		})
	}
}
