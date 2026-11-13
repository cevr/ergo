//go:build !unix

package main

import "errors"

// lockServe refuses: the daemon's lock is a Unix flock.
func lockServe(path string) (unlock func(), err error) {
	return nil, errors.New("ergo serve runs on a Unix host only")
}
