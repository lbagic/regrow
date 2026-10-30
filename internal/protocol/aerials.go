package protocol

import (
	"context"
	"errors"
	"strings"

	"github.com/lbagic/regrow/internal/aerials"
)

// aerialsEvent answers both aerials requests: the picks macOS downloads
// aerial videos for, as they stand, and for a switch what it did.
type aerialsEvent struct {
	head
	Status aerials.Status  `json:"status"`
	Result *aerials.Result `json:"result,omitempty"`
}

// aerialsStatus reads the picks. It only reads, so it holds no slot and
// answers while a scan, execute or tick runs.
func (s *session) aerialsStatus(req request) {
	if s.srv.Aerials == nil {
		s.emit(failure(req.ID, CodeAerialsFailed, "this engine cannot read the aerial picks"))
		return
	}
	go func() {
		st := s.srv.Aerials().Status(context.Background())
		s.emit(aerialsEvent{head: head{"aerials", req.ID}, Status: st})
		s.emit(done(req.ID))
	}()
}

// startAerialsSwitch turns the downloads off or on through macOS's
// administrator prompt. It holds the slot, as a tick does, so shutdown
// waits for its journal; a cancel or shutdown ends osascript, and
// whether macOS then closes a prompt still open is not measured.
func (s *session) startAerialsSwitch(req request) {
	if s.srv.Aerials == nil {
		s.emit(failure(req.ID, CodeAerialsFailed, "this engine cannot change the aerial picks"))
		return
	}
	if req.To != "off" && req.To != "on" {
		s.emit(failure(req.ID, CodeBadRequest, `aerials_switch needs "to": "off" or "on"`))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy(req) {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	op := s.begin(req, cancel)
	go func() {
		defer cancel()
		a := s.srv.Aerials()
		switchTo := a.On
		if req.To == "off" {
			switchTo = a.Off
		}
		res, err := switchTo(ctx)
		s.emit(aerialsEvent{head: head{"aerials", op.re}, Status: res.After, Result: &res})
		var terminal any = done(op.re)
		if err != nil {
			terminal = failure(op.re, CodeAerialsFailed, aerialsMessage(res, err))
		}
		s.end(op, terminal, nil)
	}()
}

// aerialsMessage says why a switch changed nothing, or did not finish.
func aerialsMessage(res aerials.Result, err error) string {
	switch {
	case errors.Is(err, aerials.ErrNotApplicable):
		switch res.Before.State {
		case aerials.StateAbsent:
			return "this Mac has no aerial database, so nothing is picked"
		case aerials.StateUnknown:
			return "the aerial picks could not be read: " + res.Before.Detail
		}
		return res.Before.Detail
	case strings.Contains(err.Error(), "(-128)"):
		// osascript's "User canceled." at the password prompt.
		return "canceled at the password prompt; nothing changed"
	}
	return err.Error()
}
