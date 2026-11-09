//go:build !linux && !darwin

package producer

import "io"

// openImage has no source of the executing image's bytes here that does not
// trust an installation pathname, so the identity stays process-scoped.
func openImage() (io.ReadCloser, error) { return nil, errUnsupported }

// imageKey: no image, so no memo key.
func imageKey(io.Reader) (string, bool) { return "", false }
