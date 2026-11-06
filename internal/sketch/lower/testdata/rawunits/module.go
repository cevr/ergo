package rawunits

import "ergo.local/prototype/internal/sketch/lower"

// Allowed: an entry function of module.go may hold the Unit.
func newBuildLowerer(u lower.Unit) int { return len(u.Files) }

// caught: any other function of module.go.
func escInModuleGo(u lower.Unit) int { return len(u.Sources) }
