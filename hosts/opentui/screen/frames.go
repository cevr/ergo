package screen

import "time"

// Frames: while the view animates, the driver asks the screen for a frame
// each period, and the screen sends an Input of kind Frame each time one
// is due. A Frame that waits unread is not sent again, so a slow reader
// takes one Frame, not a backlog.

// ticking is the screen's frame ticker (guarded by mu).
type ticking struct {
	stop    chan struct{}
	pending bool
}

// Tick sends an Input of kind Frame every period nanoseconds while period
// is above 0, and stops the frames at 0 or less. A headless screen sends
// none: a test steps its frames with the driver's Advance.
func (s *Screen) Tick(period int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ticker != nil {
		close(s.ticker.stop)
		s.ticker = nil
	}
	if period <= 0 || s.done || !s.live {
		return
	}
	t := &ticking{stop: make(chan struct{})}
	s.ticker = t
	go func() {
		every := time.NewTicker(time.Duration(period))
		defer every.Stop()
		for {
			select {
			case <-every.C:
				s.due(t)
			case <-t.stop:
				return
			}
		}
	}()
}

// due queues a Frame unless one waits unread.
func (s *Screen) due(t *ticking) {
	s.mu.Lock()
	if s.ticker != t || t.pending {
		s.mu.Unlock()
		return
	}
	t.pending = true
	s.inputs = append(s.inputs, Input{Kind: Frame})
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// took notes that the reader took a Frame (mu held).
func (s *Screen) took(in Input) {
	if in.Kind == Frame && s.ticker != nil {
		s.ticker.pending = false
	}
}
