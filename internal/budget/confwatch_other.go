//go:build unix && !linux

package budget

// watchConfig has no file watch outside Linux: an edit of the budget's
// file reaches a waiter at its next wake (an entry's end), and every new
// take at once.
func watchConfig(string, chan<- struct{}) (stop func()) { return func() {} }
