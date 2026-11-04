// Package closure holds the check that every test package of the module
// records only its own inputs, so that Go's test cache reruns a package only
// when something it depends on changes. It has no code of its own.
//
// A package's closure is the directories of `go list -deps -test` for it
// (the files Go compiles into its test binary and the files those packages
// embed), its own testdata, and a short list the check declares for it with
// a reason. A recorded input outside that set makes an unrelated edit rerun
// the package: a git grep over the tree, a directory walk, or a go build
// inside the module that stamps version-control state.
package closure
