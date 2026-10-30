package protocol

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lbagic/regrow/internal/engine"
)

type fakeSwitch struct {
	catalog []engine.Rule
	set     []string
	err     error
	// unloaded: config.yaml has not loaded since the engine started.
	unloaded error
}

func (f *fakeSwitch) Catalog() ([]engine.Rule, error) {
	if f.unloaded != nil {
		return nil, f.unloaded
	}
	return f.catalog, nil
}

func (f *fakeSwitch) State() Rules {
	st := Rules{Config: "/home/dev/.config/regrow/config.yaml"}
	for _, r := range f.catalog {
		st.Rules = append(st.Rules, RuleState{Rule: r, Runs: true})
	}
	return st
}

func (f *fakeSwitch) Set(rule string, off, beta *bool) (Rules, error) {
	if f.err != nil {
		return Rules{}, f.err
	}
	switch {
	case rule != "":
		f.set = append(f.set, rule+map[bool]string{true: " off", false: " on"}[*off])
	default:
		f.set = append(f.set, "beta "+map[bool]string{true: "on", false: "off"}[*beta])
	}
	return f.State(), nil
}

func TestRulesListAndChange(t *testing.T) {
	m := newMachine(t)
	srv := m.server()
	sw := &fakeSwitch{catalog: []engine.Rule{{ID: "npm-cache", Title: "npm cache", Risk: engine.RiskSafe}}}
	srv.Switch = sw
	p := start(t, srv)
	p.expect("hello")

	p.send(`{"type":"rules","id":"r1"}`)
	got := p.expect("rules/r1", "done/r1")[0]
	for _, want := range []string{`"catalog":[{"id":"npm-cache","title":"npm cache"`, `"off":false,"runs":true}]`, `"beta":false,"beta_flag":false,"config":"/home/dev/.config/regrow/config.yaml"`} {
		if !strings.Contains(got.raw, want) {
			t.Errorf("rules = %s, missing %s", got.raw, want)
		}
	}
	p.send(`{"type":"set_rules","id":"r2","rule":"npm-cache","off":true}`)
	p.expect("rules/r2", "done/r2")
	p.send(`{"type":"set_rules","id":"r3","beta":true}`)
	p.expect("rules/r3", "done/r3")
	if strings.Join(sw.set, ", ") != "npm-cache off, beta on" {
		t.Errorf("changes = %v", sw.set)
	}
	for _, bad := range []string{
		`{"type":"set_rules","id":"b1"}`,
		`{"type":"set_rules","id":"b1","rule":"npm-cache"}`,
		`{"type":"set_rules","id":"b1","off":true}`,
		`{"type":"set_rules","id":"b1","rule":"npm-cache","off":true,"beta":true}`,
	} {
		p.send(bad)
		p.expectError("b1", CodeBadRequest)
	}
	sw.err = errors.New("config.yaml: field of not found")
	p.send(`{"type":"set_rules","id":"r4","beta":false}`)
	if e := p.expectError("r4", CodeRulesFailed); !strings.Contains(e.Message, "field of not found") {
		t.Errorf("error = %s", e.raw)
	}

	none := start(t, newMachine(t).server())
	none.expect("hello")
	none.send(`{"type":"rules","id":"r"}`)
	none.expectError("r", CodeRulesFailed)
}

func TestAScanRunsTheSwitchsCatalog(t *testing.T) {
	m := newMachine(t)
	srv := m.server()
	srv.Switch = &fakeSwitch{catalog: []engine.Rule{{ID: "only-this", Title: "The one rule", Risk: engine.RiskSafe}}}
	var scanned []string
	srv.Scan = func(_ context.Context, rules []engine.Rule, emit func(int, engine.Finding, time.Duration)) {
		for i, r := range rules {
			scanned = append(scanned, r.ID)
			emit(i, engine.Finding{Rule: r}, 0)
		}
	}
	p := start(t, srv)
	p.expect("hello")
	p.send(`{"type":"scan","id":"s1"}`)
	p.until("s1")
	if strings.Join(scanned, " ") != "only-this" {
		t.Errorf("scanned %v, want the switch's catalog", scanned)
	}
}

func TestARuleTurnedOffSinceTheScanIsNeitherPlannedNorRun(t *testing.T) {
	m := newMachine(t)
	srv := m.server()
	sw := &fakeSwitch{catalog: srv.Catalog}
	srv.Switch = sw
	// A finding for each rule the scan runs, as a real scan reports.
	all := m.findings()
	srv.Scan = func(_ context.Context, rules []engine.Rule, emit func(int, engine.Finding, time.Duration)) {
		for i, r := range rules {
			for _, f := range all {
				if f.Rule.ID == r.ID {
					emit(i, f, time.Millisecond)
				}
			}
		}
	}
	p := start(t, srv)
	p.expect("hello")
	scanID := p.scanned("s", 2)
	planned := p.planned("p1", scanID, bothRules)

	sw.catalog = srv.Catalog[:1]
	off := srv.Catalog[1].ID
	p.send(`{"type":"plan","id":"p2","scan_id":"` + scanID + `"}`)
	if e := p.expectError("p2", CodeRulesChanged); !strings.Contains(e.Message, off) {
		t.Errorf("plan's refusal = %s, want it to name %s", e.raw, off)
	}
	p.send(`{"type":"execute","id":"x","plan_id":"` + planned.PlanID + `"}`)
	p.expectError("x", CodeRulesChanged)
	if j := m.journal(); len(j) != 0 {
		t.Errorf("journal = %v, want nothing run", j)
	}

	again := p.scanned("s2", 1)
	p.planned("p3", again, "")
}

func TestAScanWaitsForAConfigThatLoads(t *testing.T) {
	m := newMachine(t)
	srv := m.server()
	sw := &fakeSwitch{catalog: srv.Catalog, unloaded: errors.New("config.yaml: field kep not found")}
	srv.Switch = sw
	p := start(t, srv)
	p.expect("hello")
	p.send(`{"type":"scan","id":"s1"}`)
	if e := p.expectError("s1", CodeRulesFailed); !strings.Contains(e.Message, "field kep not found") {
		t.Errorf("scan's refusal = %s", e.raw)
	}
	sw.unloaded = nil
	p.scanned("s2", 2)
}
