//go:build !linux && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd

package http

import "errors"

// No watcher on this platform: every connection is watched by net/http's
// background read (engineWatcher).

type peerSys struct{}

type peerEvents struct{}

func newPeerPoll() (int, error) { return -1, errors.ErrUnsupported }

func (w *peerWatcher) start() error { return errors.ErrUnsupported }

func (w *peerWatcher) close() {}

func peerPollAdd(pfd, fd int) error { return errors.ErrUnsupported }

func peerPollDel(pfd, fd int) {}

func peerPeek(fd int) int { return peekEmpty }
