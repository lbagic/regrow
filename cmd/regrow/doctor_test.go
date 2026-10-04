package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lbagic/regrow/internal/engine"
)

func TestDoctorTextShowsThreeVerdicts(t *testing.T) {
	doc := func(id string, flag int64, items ...engine.Item) engine.Finding {
		return engine.Finding{
			Rule:  engine.Rule{ID: id, Title: id, Risk: engine.RiskSafe, Doctor: &engine.Doctor{FlagAbove: engine.ByteSize(flag), Story: "known bug"}},
			Items: items,
		}
	}
	rep := engine.BuildDoctorReport([]engine.Finding{
		doc("runaway", 1<<30, engine.Item{Bytes: 3 << 30, Partial: true}),
		doc("healthy", 1<<30, engine.Item{Bytes: 1 << 20}),
		doc("gated", 1<<30, engine.Item{Partial: true}),
		doc("half-read", 1<<30, engine.Item{Bytes: 1 << 20, Partial: true}),
	}, nil)
	var buf bytes.Buffer
	writeDoctorReport(&buf, rep)
	out := buf.String()
	for _, want := range [][]string{
		{"runaway", "≥ 3.0 GiB", "RUNAWAY"},
		{"✓", "healthy", "normal"},
		{"?", "gated", "unreadable", "unknown", "Full Disk Access may be needed"},
		{"?", "half-read", "≥ 1.0 MiB", "unknown", "some folders unreadable"},
		{"1 runaway bug(s) found"},
	} {
		if !lineWith(out, want...) {
			t.Errorf("no line with %q in:\n%s", want, out)
		}
	}
	if lineWith(out, "✓", "gated") || lineWith(out, "✓", "half-read") {
		t.Errorf("a partial check under the line must not read healthy:\n%s", out)
	}
}

func causeRow(check string, verdict engine.Verdict, detail string) engine.CauseCheck {
	return engine.CauseCheck{
		RuleID:  "owner",
		Verdict: verdict,
		Detail:  detail,
		Cause: engine.Cause{
			Check: check, Title: "Title of " + check, Story: "story of " + check,
			Fix: []string{"first step of " + check, "second step of " + check},
		},
	}
}

func TestDoctorTextCauseRows(t *testing.T) {
	rep := engine.BuildDoctorReport(nil, []engine.CauseCheck{
		causeRow("in-effect", engine.VerdictFlagged, "15 videos, 8.4 GiB"),
		causeRow("fine", engine.VerdictNormal, "capped at 4.0 GiB"),
		causeRow("unread", engine.VerdictUnknown, "settings unreadable"),
	})
	var buf bytes.Buffer
	writeDoctorReport(&buf, rep)
	out := buf.String()
	for _, want := range [][]string{
		{"FIX THE CAUSE", "changes nothing"},
		{"🚩", "Title of in-effect", "15 videos, 8.4 GiB"},
		{"story of in-effect"},
		{"fix:", "first step of in-effect"},
		{"second step of in-effect"},
		{"✓", "Title of fine", "capped at 4.0 GiB"},
		{"?", "Title of unread", "unknown", "settings unreadable"},
		{"1 cause(s) to fix above", "1 more could not be checked", "regrow changes no setting"},
	} {
		if !lineWith(out, want...) {
			t.Errorf("no line with %q in:\n%s", want, out)
		}
	}
	// A cause that is not in effect prints its row, not its fix.
	for _, absent := range []string{"story of fine", "step of fine", "story of unread", "step of unread", "nothing needs fixing"} {
		if strings.Contains(out, absent) {
			t.Errorf("output must not hold %q:\n%s", absent, out)
		}
	}
	if strings.Index(out, "HERO BUGS") > strings.Index(out, "FIX THE CAUSE") || strings.Index(out, "FIX THE CAUSE") > strings.Index(out, "PHANTOM SPACE") {
		t.Errorf("sections out of order:\n%s", out)
	}
}

func TestDoctorTextWithoutCauses(t *testing.T) {
	tests := []struct {
		name   string
		causes []engine.CauseCheck
		want   []string
		absent []string
	}{
		{"no rows declared", nil, []string{"nothing needs fixing"}, []string{"FIX THE CAUSE"}},
		{"every cause clear", []engine.CauseCheck{causeRow("fine", engine.VerdictNormal, "set")}, []string{"FIX THE CAUSE", "nothing needs fixing"}, []string{"cause(s) to fix"}},
		{"only unknown", []engine.CauseCheck{causeRow("unread", engine.VerdictUnknown, "unreadable")}, []string{"1 cause check(s) unknown above"}, []string{"cause(s) to fix", "nothing needs fixing"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			writeDoctorReport(&buf, engine.BuildDoctorReport(nil, tt.causes))
			out := buf.String()
			for _, want := range tt.want {
				if !strings.Contains(out, want) {
					t.Errorf("output lacks %q:\n%s", want, out)
				}
			}
			for _, absent := range tt.absent {
				if strings.Contains(out, absent) {
					t.Errorf("output must not hold %q:\n%s", absent, out)
				}
			}
		})
	}
}

func TestDoctorJSONCarriesCauseRows(t *testing.T) {
	raw, err := json.Marshal(engine.BuildDoctorReport(nil, []engine.CauseCheck{causeRow("in-effect", engine.VerdictFlagged, "seen")}))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Causes []struct {
			RuleID  string `json:"rule_id"`
			Verdict string `json:"verdict"`
			Detail  string `json:"detail"`
			Cause   struct {
				Check string   `json:"check"`
				Fix   []string `json:"fix"`
			} `json:"cause"`
		} `json:"causes"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Causes) != 1 {
		t.Fatalf("causes = %s", raw)
	}
	c := got.Causes[0]
	if c.RuleID != "owner" || c.Verdict != "flagged" || c.Detail != "seen" || c.Cause.Check != "in-effect" || len(c.Cause.Fix) != 2 {
		t.Errorf("cause row = %+v in %s", c, raw)
	}
}
