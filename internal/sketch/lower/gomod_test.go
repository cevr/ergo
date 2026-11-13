package lower

// goMod is the go.mod of a program with no Go requirements, for the tests
// that write an emitted module by hand. It lives with the tests: its value
// reads every runtime source (RuntimeGoModule), which no ergo process
// should pay for at start.
var goMod = string(goModFile(GoModule{}))
