// Package apigen is `ergo gen api` (docs/spec/api.md, ergo gen api): it
// writes the hub of an operation type, the Ergo package <pkg>api beside the
// contract's package, which holds the operation type as data (Request, Op,
// Reply, Failure, Outcome), Call, the one dispatch, Client, a provider over
// any way to send a Request, and the laws CallIsTheOperation and
// ClientIsCall. Every transport (`ergo gen rpc`, `cli`, `mcp`) is written
// over the hub.
//
// The generator's code is rpcgen.GenerateAPI, beside the contract reader
// and digest every generator shares: rpcgen's own tests generate a hub
// before its rpc package, and a Go test may not import a package that
// imports the package under test.
package apigen

import (
	"ergo.local/prototype/internal/rpcgen"
	"ergo.local/prototype/internal/sketch/check"
)

// Files is the generated package: its folder, relative to the contract's
// package folder, and its files by name.
type Files = rpcgen.Files

// Generate writes the hub of the operation type opName of pkg, whose import
// path is path. It admits the contracts `ergo gen rpc` admits.
func Generate(pkg *check.Package, path, opName string) (Files, error) {
	return rpcgen.GenerateAPI(pkg, path, opName)
}

// Dir is the hub's folder name for the contract package named pkgName.
func Dir(pkgName string) string { return rpcgen.APIDir(pkgName) }
