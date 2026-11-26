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
//go:embed prelude.ergo builtin.ergo collections/*.ergo option/*.ergo core/*.ergo timeout/*.ergo task/*.ergo scope/*.ergo slices/*.ergo queue/*.ergo log/*.ergo trace/*.ergo strings/*.ergo strconv/*.ergo
//go:embed os/*.ergo signal/*.ergo
//go:embed fs/*.ergo fs/mapped/*.ergo fstest/*.ergo
//go:embed io/*.ergo term/*.ergo prompt/*.ergo ansi/*.ergo
//go:embed utf8/*.ergo url/*.ergo
//go:embed flag/*.ergo api/*.ergo
//go:embed maps/*.ergo cmp/*.ergo set/*.ergo
//go:embed rand/*.ergo
//go:embed sync/*.ergo
//go:embed embed/*.ergo
//go:embed atomic/*.ergo pace/*.ergo
//go:embed filepath/*.ergo
//go:embed exec/*.ergo
//go:embed math/*.ergo bits/*.ergo decimal/*.ergo
//go:embed hex/*.ergo base64/*.ergo sha256/*.ergo crc32/*.ergo sha512/*.ergo sha1/*.ergo sha1dc/*.ergo hmac/*.ergo uuid/*.ergo
//go:embed machines/*.ergo
//go:embed csv/*.ergo codec/*.ergo json/*.ergo toml/*.ergo rpc/*.ergo config/*.ergo
//go:embed jsonschema/*.ergo
//go:embed mcp/*.ergo mcp/memory/*.ergo mcp/stdio/*.ergo mcp/httpmcp/*.ergo
//go:embed testing/*.ergo explore/*.ergo gen/*.ergo temporal/*.ergo
//go:embed tasks/*.ergo pubsub/*.ergo stream/*.ergo
//go:embed schedule/*.ergo cron/*.ergo process/*.ergo
//go:embed http/*.ergo net/*.ergo nettest/*.ergo
//go:embed sql/*.ergo sqltest/*.ergo
//go:embed ratelimit/*.ergo ratelimit/memory/*.ergo
//go:embed kv/*.ergo kv/memory/*.ergo kv/file/*.ergo
//go:embed jobs/*.ergo jobs/memory/*.ergo jobs/file/*.ergo
//go:embed session/*.ergo session/memory/*.ergo session/file/*.ergo
//go:embed workflow/*.ergo workflow/memory/*.ergo workflow/file/*.ergo
//go:embed totp/*.ergo proof/*.ergo proof/memory/*.ergo proof/file/*.ergo
//go:embed actor/*.ergo
//go:embed journal/*.ergo journal/memory/*.ergo journal/file/*.ergo
//go:embed llm/*.ergo llm/memory/*.ergo llm/recorded/*.ergo
//go:embed time/*.ergo civil/*.ergo tz/*.ergo tz/iana/*.ergo tz/host/*.ergo cache/*.ergo
//go:embed arena/*.ergo dense/*.ergo
//go:embed ui/*.ergo
//go:embed text/*.ergo
//go:embed llm/*.ergo llm/memory/*.ergo
//go:embed lint/*.ergo
//go:embed bench/*.ergo
//go:embed ui/layout/*.ergo
//go:embed semver/*.ergo
//go:embed code/*.ergo
//go:embed graph/*.ergo codegraph/*.ergo
//go:embed html/*.ergo xml/*.ergo
//go:embed build/*.ergo
//go:embed glob/*.ergo
//go:embed color/*.ergo
//go:embed yaml/*.ergo
//go:embed zlib/*.ergo
var Sources embed.FS

// LedgerText is the std law ledger (runtime.md, Tests 10), as
// `go run ./scripts/stdlaws` writes it. Only stdlib reads it.
//
//go:embed ergo.laws
var LedgerText string

// TestRedText is the shipped standard-library test red list.
//
//go:embed std-red.txt
var TestRedText string
