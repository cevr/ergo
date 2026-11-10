// Package tstools holds the local TypeScript tool sources for the opt-in
// conformance harness. Embedding makes them tracked inputs of Go's test cache.
package tstools

import "embed"

// Sources contains the loaders and the reviewed dependency pins. No
// generated TypeScript or installed dependencies are embedded.
//
//go:embed *.mjs package.json bun.lock
var Sources embed.FS
