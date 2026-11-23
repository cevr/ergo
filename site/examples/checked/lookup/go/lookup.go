// Go 1.27. The same program as lookup.ergo.
package hosts

import (
	"context"
	"errors"
	"time"
)

type Answer struct {
	Name string
	Addr string
}

type NotFoundError struct{ Name string }

func (e *NotFoundError) Error() string { return "no host " + e.Name }

type Resolver interface {
	Resolve(ctx context.Context, name string) (Answer, error)
}

func Lookup(ctx context.Context, r Resolver, name string) (Answer, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return r.Resolve(ctx, name)
}

func Address(ctx context.Context, r Resolver, name string) (string, error) {
	answer, err := Lookup(ctx, r, name)
	var notFound *NotFoundError
	switch {
	case err == nil:
		return answer.Addr, nil
	case errors.As(err, &notFound):
		return "unknown", nil
	case errors.Is(err, context.DeadlineExceeded):
		return "timed-out", nil
	default:
		return "", err
	}
}
