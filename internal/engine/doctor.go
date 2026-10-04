package engine

// Doctor mode (Prompt H, plans/2026-07-13-doctor-phantom.md): scan
// only the hero-bug rules and the phantom-space category, then split
// the findings into a two-section report. Selection and assembly are
// pure functions here; cmd owns printing.

// CategoryPhantomSpace is the category whose rules explain space
// Finder counts but du cannot find (TM snapshots, sparse VM disks,
// purgeable). They ride along in every doctor run.
const CategoryPhantomSpace = "phantom-space"

// DoctorRules selects the catalog subset a doctor run scans: every
// rule carrying a doctor block plus the whole phantom-space category.
func DoctorRules(catalog []Rule) []Rule {
	var out []Rule
	for _, r := range catalog {
		if r.Doctor != nil || r.Category == CategoryPhantomSpace {
			out = append(out, r)
		}
	}
	return out
}

// Verdict is a hero check's outcome against its healthy/runaway line.
type Verdict string

const (
	// VerdictFlagged: the total, even as a lower bound, is over the
	// line — the known bug's signature, worth the story and a fix.
	VerdictFlagged Verdict = "flagged"
	// VerdictNormal: the total is complete and at or under the line.
	VerdictNormal Verdict = "normal"
	// VerdictUnknown: under the line, but some of it could not be
	// read (or the rule failed), so the real total may be over.
	VerdictUnknown Verdict = "unknown"
)

// HeroCheck is one hero-bug verdict: the rule's measured total against
// its healthy/runaway line.
type HeroCheck struct {
	Finding Finding `json:"finding"`
	Verdict Verdict `json:"verdict"`
}

// heroVerdict judges a finding against its doctor line. A lower bound
// over the line is proof enough to flag; under the line it proves
// nothing.
func heroVerdict(f Finding) Verdict {
	switch {
	case f.TotalBytes() > int64(f.Rule.Doctor.FlagAbove):
		return VerdictFlagged
	case f.Err != "" || f.Partial():
		return VerdictUnknown
	default:
		return VerdictNormal
	}
}

// DoctorReport is the assembled report: hero verdicts in catalog
// order, then the phantom-space findings with their explainer copy.
type DoctorReport struct {
	Hero    []HeroCheck `json:"hero"`
	Phantom []Finding   `json:"phantom"`
}

// BuildDoctorReport splits doctor-scan findings into the report. A
// rule can appear in both sections only by carrying a doctor block
// inside phantom-space; today the sections are disjoint.
func BuildDoctorReport(findings []Finding) DoctorReport {
	var rep DoctorReport
	for _, f := range findings {
		if f.Rule.Doctor != nil {
			rep.Hero = append(rep.Hero, HeroCheck{Finding: f, Verdict: heroVerdict(f)})
		}
		if f.Rule.Category == CategoryPhantomSpace {
			rep.Phantom = append(rep.Phantom, f)
		}
	}
	return rep
}
