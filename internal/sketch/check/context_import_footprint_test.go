package check_test

import (
	"ergo.local/prototype/internal/sketch/check"
)

// importedImporter serves one checked package b, and the standard library.
type importedImporter struct{ b *check.Package }

func (i importedImporter) Import(path string) (*check.Package, error) {
	if path == "b" {
		return i.b, nil
	}
	return check.Std.Import(path)
}
