package cli

import (
	"fmt"
	"io"
	"path/filepath"

	"github.com/kislow/kio/internal/ki"
)

// plannerInput is what a read-only planner pick looks at: the ledger and the
// pending reservations, so a daily cap holds the same as for a delegate pick.
func (a *App) plannerInput() (ki.PickInput, error) {
	ledger, err := ki.ReadLedger(filepath.Join(a.jobsDir(), ki.LedgerFile))
	if err != nil {
		return ki.PickInput{}, fmt.Errorf("reading the job ledger: %w", err)
	}
	reserved, err := ki.ReadReservations(a.jobsDir())
	if err != nil {
		return ki.PickInput{}, fmt.Errorf("reading the pick reservations: %w", err)
	}
	return ki.PickInput{Ledger: ledger, Reserved: reserved, Now: a.now(), Installed: installed}, nil
}

// resolveSession is the session's agent for the flag and KI_AGENT given. The
// ledger is read only when roles.planner decides.
func (a *App) resolveSession(flag string, m *ki.Machine) (ki.SessionAgent, error) {
	envAgent := a.Getenv("KI_AGENT")
	var in ki.PickInput
	if _, ok := m.RoleFor(ki.RolePlanner); ok && flag == "" && envAgent == "" {
		var err error
		if in, err = a.plannerInput(); err != nil {
			return ki.SessionAgent{}, err
		}
	}
	sa, err := ki.ResolveSessionAgent(flag, envAgent, m, in)
	if err != nil {
		return sa, fail("%v", err)
	}
	return sa, nil
}

// printSkipped names each provider a pick passed over, and why, on w.
func printSkipped(w io.Writer, role string, skipped []ki.Skipped) {
	for _, s := range skipped {
		verb := "skipped"
		if s.Demoted {
			verb = "demoted"
		}
		fmt.Fprintf(w, "ki: role %s: %s %s, %s\n", role, s.Provider.Name(), verb, s.Reason)
	}
}
