//go:build race || ergo_stockpark

package rt

// parker is the stock body (park.go): a race build, a build with the tag
// ergo_stockpark, and the reference the laws compare the word body with.
type parker = stockParker
