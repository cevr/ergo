//go:build !unix

package main

import "errors"

// lockDev refuses: the daemon's lock is a Unix flock.
func lockDev(path string) (unlock func(), err error) {
	return nil, errors.New("ergo dev runs on a Unix host only")
}
