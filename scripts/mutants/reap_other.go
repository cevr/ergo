//go:build !linux

package main

// The subreaper is Linux's (reap_linux.go); elsewhere a child's orphans
// are init's, and only stopGroup stops what a child leaves.
func subreaper()       {}
func starting() func() { return func() {} }
func track(int)        {}
func untrack(int)      {}
func reapAfter(string) {}
