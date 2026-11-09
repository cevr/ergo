package lower

import (
	"ergo.local/prototype/internal/sketch/check"
)

type contextImporter struct{ pkg *check.Package }

func (i contextImporter) Import(path string) (*check.Package, error) {
	if path == "makers" {
		return i.pkg, nil
	}
	return check.Std.Import(path)
}
