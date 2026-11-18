module ergo.local/prototype/hosts/opentui

go 1.27

require github.com/ebitengine/purego v0.11.1

// The repository's module, for its C binder (internal/bindc), which writes
// ot/opentui.go. Go's internal rule is by import path, and this module's
// path is under ergo.local/prototype.
require ergo.local/prototype v0.0.0

replace ergo.local/prototype => ../..
