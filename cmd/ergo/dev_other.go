//go:build !unix

package main

import "errors"

// lockDev refuses: the daemon's lock is a Unix flock.
func lockDev(path string) (unlock func(), err error) {
	return nil, errors.New("ergo dev runs on a Unix host only")
}

// viaShortPath calls use with path: the daemon runs on a Unix host only.
func viaShortPath(path string, use func(string) error) error { return use(path) }
