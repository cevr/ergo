//go:build !unix

package budget

import (
	"context"
	"errors"
	"io"
)

// Request is a take: its width in cores, its class, its lane, its memory
// and its command's key.
type Request struct {
	Width  int
	Crit   bool
	Lane   string
	Memory int64
	Key    string
}

// Budget is unsupported where there is no flock.
type Budget struct{}

var errUnsupported = errors.New("budget: not supported on this system")

// Open fails: the budget needs flock.
func Open(Config) (*Budget, error) { return nil, errUnsupported }

// Run fails: the budget needs flock.
func Run(context.Context, *Budget, Request, []string, io.Reader, io.Writer, io.Writer) (int, error) {
	return 1, errUnsupported
}
