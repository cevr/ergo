//go:build !linux && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd

package http

import "errors"

// No watcher on this platform: every connection is watched by net/http's
// background read (engineWatcher).

type peerEvents struct{}

func newPeerPoll() (int, error) { return -1, errors.ErrUnsupported }

func peerPollAdd(pfd, fd int) error { return errors.ErrUnsupported }

func peerPollDel(pfd, fd int) {}

func peerPollWait(pfd int, ev *peerEvents, fds []int32) ([]int32, error) {
	return fds, errors.ErrUnsupported
}

func peerPeek(fd int) int { return peekEmpty }
