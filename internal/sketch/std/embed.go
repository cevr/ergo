// Package std holds the Ergo source of the language's prelude and
// standard library, embedded, and nothing else: its API is
// internal/sketch/stdlib. Go recompiles every importer of a package whose
// embedded files changed, so the importer is only stdlib, whose compiled
// form a std edit does not change; check, lower and the rest import stdlib
// and are not recompiled (gate-speed GS25). scripts/gs25-move-std-api keeps
// it so.
package std

import "embed"

// Sources are the std packages' .ergo files, by path under this directory.
// Only stdlib reads them.
//
//go:embed prelude.ergo timeout/*.ergo task/*.ergo scope/*.ergo slices/*.ergo queue/*.ergo log/*.ergo trace/*.ergo strings/*.ergo strconv/*.ergo
//go:embed os/*.ergo
//go:embed fs/*.ergo fstest/*.ergo
//go:embed io/*.ergo term/*.ergo prompt/*.ergo
//go:embed utf8/*.ergo
//go:embed flag/*.ergo
//go:embed maps/*.ergo cmp/*.ergo
//go:embed time/*.ergo
//go:embed rand/*.ergo
//go:embed sync/*.ergo
//go:embed embed/*.ergo
//go:embed atomic/*.ergo
//go:embed arena/*.ergo
//go:embed filepath/*.ergo
//go:embed exec/*.ergo
//go:embed math/*.ergo bits/*.ergo
//go:embed hex/*.ergo base64/*.ergo sha256/*.ergo crc32/*.ergo
//go:embed machines/*.ergo
//go:embed csv/*.ergo codec/*.ergo json/*.ergo toml/*.ergo rpc/*.ergo config/*.ergo
//go:embed testing/*.ergo explore/*.ergo
//go:embed tasks/*.ergo
//go:embed schedule/*.ergo
//go:embed http/*.ergo
//go:embed actor/*.ergo
//go:embed journal/*.ergo journal/memory/*.ergo journal/file/*.ergo
var Sources embed.FS
