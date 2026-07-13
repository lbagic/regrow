package engine

import "testing"

func doctorRule(id string, flagAbove ByteSize) Rule {
	r := validRule()
	r.ID = id
	r.Doctor = &Doctor{FlagAbove: flagAbove, Story: "known bug"}
	return r
}

func phantomRule(id string) Rule {
	r := validRule()
	r.ID = id
	r.Category = CategoryPhantomSpace
	r.Risk = RiskSurfaceOnly
	return r
}

func TestDoctorRulesSelection(t *testing.T) {
	plain := validRule()
	catalog := []Rule{plain, doctorRule("hero", 1 << 30), phantomRule("phantom")}
	got := DoctorRules(catalog)
	if len(got) != 2 || got[0].ID != "hero" || got[1].ID != "phantom" {
		t.Fatalf("DoctorRules selected %+v, want [hero phantom]", got)
	}
}

func TestBuildDoctorReport(t *testing.T) {
	hero := doctorRule("hero", 1<<30)
	phantom := phantomRule("phantom")
	findings := []Finding{
		{Rule: hero, Items: []Item{{Bytes: 2 << 30}}},
		{Rule: phantom, Items: []Item{{Bytes: 5}}},
	}
	rep := BuildDoctorReport(findings)
	if len(rep.Hero) != 1 || len(rep.Phantom) != 1 {
		t.Fatalf("report sections: hero=%d phantom=%d, want 1/1", len(rep.Hero), len(rep.Phantom))
	}
	if !rep.Hero[0].Flagged {
		t.Fatal("2GiB over a 1GiB line must flag")
	}

	// At exactly the line: healthy. The threshold is "grew past", not
	// "reached" — a normal cache sitting at its round number must not
	// scream about a bug.
	at := BuildDoctorReport([]Finding{{Rule: hero, Items: []Item{{Bytes: 1 << 30}}}})
	if at.Hero[0].Flagged {
		t.Fatal("total equal to flag_above must not flag")
	}

	// Absent target (no items): healthy, present in the report so the
	// checklist shows what was looked for.
	empty := BuildDoctorReport([]Finding{{Rule: hero}})
	if len(empty.Hero) != 1 || empty.Hero[0].Flagged {
		t.Fatalf("empty finding must be an unflagged check, got %+v", empty.Hero)
	}
}
