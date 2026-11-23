package screen

import (
	"fmt"

	"ergo.local/prototype/hosts/opentui/ot"
)

// Notify says text on the terminal's notification channel (OpenTUI's
// ot_session_notification: OSC 9, or the terminal's own protocol, where
// the terminal takes notifications; nothing where it does not). It is a
// terminal's live region (gap 7): the driver says each text a pass newly
// shows in a ui.LiveNode, as ui.TestHost's Announced logs it. A headless
// screen only keeps the text (Said). The text goes out at once, behind
// the frames already written (the driver says it after it presents the
// pass that wrote it).
func (s *Screen) Notify(text string) error {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return fail(CallClosed, 0, ErrClosed)
	}
	s.mu.Unlock()
	if err := check(CallNotify, 0); err != nil {
		return err
	}
	s.mu.Lock()
	s.said = append(s.said, text)
	live := s.live
	s.mu.Unlock()
	if !live {
		return nil
	}
	var err error
	s.do(func() {
		s.calls++
		if _, err = ot.SessionNotification(s.ctx, s.session, []byte(text), nil); err != nil {
			err = fmt.Errorf("screen: notification: %w", err)
			return
		}
		err = s.flush()
	})
	return wrap(CallNotify, 0, err)
}

// reply hands a terminal's reply to the setup's capability queries to the
// session (owner thread), which learns from it what the terminal takes:
// notifications among the rest. A reply the session does not take (the
// setup is over, or it is no capability reply) changes nothing.
func (s *Screen) reply(raw []byte) {
	if !s.live || len(raw) == 0 {
		return
	}
	s.calls++
	// ergo-lint: a rejected reply is one the session has no use for.
	_ = ot.SessionControl(s.ctx, s.session, ot.SessionControlOptions{Kind: ot.OT_CONTROL_CAPABILITY_RESPONSE}, raw)
}

// Said is each text Notify said, in order.
func (s *Screen) Said() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.said...)
}
