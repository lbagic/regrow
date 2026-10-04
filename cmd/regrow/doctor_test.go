package main

import (
	"bytes"
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
	})
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
