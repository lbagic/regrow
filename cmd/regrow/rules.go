package main

import (
	"fmt"
	"strings"
	"sync"

	"github.com/lbagic/regrow/internal/config"
	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/protocol"
)

// listedRule is a rule as `regrow rules` lists it, off when config.yaml
// turns it off.
type listedRule struct {
	engine.Rule
	Off bool `json:"off,omitempty"`
}

// commandCatalog is the catalog a command runs: the beta rules only with
// --beta-rules or config.yaml's `rules: beta`, and none config.yaml
// turns off. A command that names an off rule is refused: off means
// regrow does not look there, and naming the rule is no way round that.
func commandCatalog(cmd string, ids []string, betaFlag bool, catalog []engine.Rule, rules config.Rules, path string) ([]engine.Rule, error) {
	if !betaFlag && !rules.Beta {
		catalog = engine.WithoutBeta(catalog)
	}
	for _, id := range ids {
		var named string
		switch cmd {
		case "clean", "plan":
			named, _, _ = strings.Cut(id, "/")
		case "prune":
			for _, off := range rules.Off {
				if id == off || id == strings.TrimSuffix(off, "-cache") {
					named = off
				}
			}
		}
		if named != "" && rules.IsOff(named) {
			return nil, fmt.Errorf("%s is turned off in %s (rules: off): turn it on there, or in the app's Rules", named, path)
		}
	}
	return withoutOff(catalog, rules), nil
}

// withoutOff drops the rules config.yaml turns off.
func withoutOff(catalog []engine.Rule, rules config.Rules) []engine.Rule {
	out := make([]engine.Rule, 0, len(catalog))
	for _, r := range catalog {
		if !rules.IsOff(r.ID) {
			out = append(out, r)
		}
	}
	return out
}

func printRules(catalog []engine.Rule, rules config.Rules, asJSON bool) error {
	listed := make([]listedRule, len(catalog))
	for i, r := range catalog {
		listed[i] = listedRule{Rule: r, Off: rules.IsOff(r.ID)}
	}
	if asJSON {
		return emitJSON(listed)
	}
	for _, r := range listed {
		title := r.Title
		if r.Off {
			title += " (off)"
		}
		fmt.Printf("%-24s %-12s %-10s %s\n", r.ID, r.Category, r.Risk, title)
	}
	return nil
}

// ruleSwitch is the engine's catalog: the whole catalog, beta rules
// included, run as config.yaml's `rules:` says. It reads the file again
// whenever a scan or tick starts, so an edit by hand counts as one made
// in the app; a file that stops loading leaves the rules as it last
// loaded and says why.
type ruleSwitch struct {
	all      []engine.Rule
	betaFlag bool
	path     string

	mu    sync.Mutex
	rules config.Rules
	err   error
}

func newRuleSwitch(all []engine.Rule, betaFlag bool, path string, rules config.Rules) *ruleSwitch {
	return &ruleSwitch{all: all, betaFlag: betaFlag, path: path, rules: rules}
}

// read takes the file as it is now. The caller holds mu.
func (s *ruleSwitch) read() {
	c, err := config.LoadFile(s.path)
	s.err = err
	if err == nil {
		s.rules = c.Rules
	}
}

func (s *ruleSwitch) Catalog() []engine.Rule {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.read()
	catalog, _ := commandCatalog("engine", nil, s.betaFlag, s.all, s.rules, s.path)
	return catalog
}

func (s *ruleSwitch) State() protocol.Rules {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.read()
	return s.state()
}

// state is the rules as they stand. The caller holds mu.
func (s *ruleSwitch) state() protocol.Rules {
	st := protocol.Rules{Rules: make([]protocol.RuleState, len(s.all)), Beta: s.rules.Beta, BetaFlag: s.betaFlag, Config: s.path}
	for i, r := range s.all {
		off := s.rules.IsOff(r.ID)
		st.Rules[i] = protocol.RuleState{Rule: r, Off: off, Runs: !off && (!r.Beta || s.betaFlag || s.rules.Beta)}
	}
	if s.err != nil {
		st.Error = s.err.Error()
	}
	return st
}

func (s *ruleSwitch) Set(rule string, off, beta *bool) (protocol.Rules, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.read()
	if s.err != nil {
		return protocol.Rules{}, s.err
	}
	next := config.Rules{Off: append([]string(nil), s.rules.Off...), Beta: s.rules.Beta}
	if beta != nil {
		next.Beta = *beta
	}
	if rule != "" {
		known := false
		for _, r := range s.all {
			known = known || r.ID == rule
		}
		if !known {
			return protocol.Rules{}, fmt.Errorf("no rule %q in the catalog", rule)
		}
		kept := next.Off[:0]
		for _, id := range next.Off {
			if id != rule {
				kept = append(kept, id)
			}
		}
		next.Off = kept
		if *off {
			next.Off = append(next.Off, rule)
		}
	}
	if err := config.SetRules(s.path, next); err != nil {
		return protocol.Rules{}, err
	}
	s.read()
	return s.state(), s.err
}
