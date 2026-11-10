// Package jobs contains the example's source for the opt-in peer acceptance
// check. Embedding records these inputs in the check's ordinary Go dependency
// closure without giving the check a recursive read of the checkout.
package jobs

import "embed"

// Sources is the neutral jobs program, including its served HTTP contract.
//
//go:embed ergo.mod api jobs memory cmd
var Sources embed.FS
