package scanner

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/lbagic/regrow/internal/aerials"
	"github.com/lbagic/regrow/internal/engine"
)

// HoldCheck says why a rule's items are kept now, or "" when they may
// be offered. An error keeps them too.
type HoldCheck func(ctx context.Context) (why string, err error)

// DefaultHolds returns the built-in hold checks for the host.
func DefaultHolds(host engine.Host) map[string]HoldCheck {
	return map[string]HoldCheck{
		engine.HoldAerialPicks: func(ctx context.Context) (string, error) { return aerialPicksHold(ctx, host) },
	}
}

// held runs the rule's hold check under the query deadline. A check
// that is unknown, fails or gives no answer keeps the items.
func (s *Scanner) held(ctx context.Context, r engine.Rule) string {
	check, known := s.Holds[r.Hold]
	if !known {
		return fmt.Sprintf("Kept: the hold %q has no check in this regrow.", r.Hold)
	}
	why, err := withDeadline(ctx, s.timeout(), "the hold check", check)
	if err != nil {
		return "Kept while what holds it cannot be read: " + err.Error() + "."
	}
	return why
}

// RecheckHold is the hold-recheck pre-action: right before an action
// of a held rule runs, it runs the hold check again and refuses while
// it holds or cannot be read.
func RecheckHold(ctx context.Context, host engine.Host, r engine.Rule) error {
	s := &Scanner{Host: host, Holds: DefaultHolds(host)}
	if why := s.held(ctx, r); why != "" {
		return errors.New(why)
	}
	return nil
}

// aerialPicksHold keeps the aerial videos while macOS has anything
// picked to download them for: deleted then, they come back.
func aerialPicksHold(ctx context.Context, host engine.Host) (string, error) {
	db := aerials.DB
	if host.Root != "" && host.Root != "/" {
		db = filepath.Join(host.Root, db)
	}
	st := (&aerials.Aerials{DB: db, Version: host.Version}).Status(ctx)
	switch st.State {
	case aerials.StateOff, aerials.StateAbsent:
		return "", nil
	case aerials.StateOn:
		return "Kept while macOS downloads aerial videos for what is picked (" + st.Counts.Text() + "): deleted now, they come back. To clear the picks first: `regrow aerials off`.", nil
	case aerials.StateUnmeasured:
		return "Kept: " + st.Detail + ", so whether deleted videos would come back is not known.", nil
	}
	return "Kept while the aerial picks cannot be read: " + st.Detail + ".", nil
}
