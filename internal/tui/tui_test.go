package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lbagic/regrow/internal/engine"
)

// fixtureFindings covers every row shape the checklist renders: safe
// (auto-selected), caution, surface-only, scan error, and an empty
// finding that must be filtered out.
func fixtureFindings(home string) []engine.Finding {
	return []engine.Finding{
		{
			Rule: engine.Rule{
				ID: "go-build-cache", Title: "Go build cache", Category: "dev-caches",
				Risk: engine.RiskSafe, NativeCommand: engine.Argv{"go", "clean", "-cache"},
				Regen: engine.Regen{Story: "Repopulated by the next go build.", Cost: "next builds slower once"},
			},
			Items: []engine.Item{{Path: home + "/Library/Caches/go-build", Bytes: 20 << 30}},
		},
		{
			Rule: engine.Rule{
				ID: "go-mod-cache", Title: "Go module cache", Category: "dev-caches",
				Risk: engine.RiskCaution, NativeCommand: engine.Argv{"go", "clean", "-modcache"},
				Regen: engine.Regen{Story: "Modules re-download on demand.", Cost: "network"},
			},
			Items: []engine.Item{{Path: home + "/go/pkg/mod", Bytes: 17 << 30}},
		},
		{
			Rule: engine.Rule{
				ID: "ios-backups", Title: "iOS device backups", Category: "surface",
				Risk:  engine.RiskSurfaceOnly,
				Regen: engine.Regen{Story: "Finder ▸ Manage Backups.", Cost: "n/a"},
			},
			Items: []engine.Item{{Path: home + "/Library/Application Support/MobileSync/Backup", Bytes: 23 << 30}},
		},
		{
			Rule: engine.Rule{
				ID: "docker-cache", Title: "Docker build cache", Category: "dev-caches",
				Risk: engine.RiskSafe, NativeCommand: engine.Argv{"docker", "builder", "prune", "-f"},
			},
			Err: "docker: command not found",
		},
		{
			Rule: engine.Rule{
				ID: "empty-rule", Title: "Nothing here", Category: "dev-caches",
				Risk: engine.RiskSafe,
			},
		},
	}
}

// newTestModel builds a model and feeds it the fixture scan result.
func newTestModel(t *testing.T) Model {
	t.Helper()
	host := engine.Host{OS: "darwin", Version: "15.5", Home: "/Users/fixture"}
	m := New(host, "test", func(context.Context) []engine.Finding {
		return fixtureFindings(host.Home)
	})
	next, _ := m.Update(scanDoneMsg{findings: fixtureFindings(host.Home)})
	return next.(Model)
}

func key(s string) tea.KeyMsg {
	if s == " " {
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	}
	if s == "enter" {
		return tea.KeyMsg{Type: tea.KeyEnter}
	}
	if s == "esc" {
		return tea.KeyMsg{Type: tea.KeyEsc}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func press(t *testing.T, m Model, keys ...string) Model {
	t.Helper()
	for _, k := range keys {
		next, _ := m.Update(key(k))
		m = next.(Model)
	}
	return m
}

func TestListGroupsAndRanks(t *testing.T) {
	view := newTestModel(t).View()

	// Categories size-ranked: dev-caches (37 GiB) before surface (23 GiB).
	dev := strings.Index(view, "DEV CACHES")
	surface := strings.Index(view, "SURFACE")
	if dev == -1 || surface == -1 || dev > surface {
		t.Fatalf("want DEV CACHES header before SURFACE, got:\n%s", view)
	}
	// Inside dev-caches, larger finding first.
	build := strings.Index(view, "Go build cache")
	mod := strings.Index(view, "Go module cache")
	if build == -1 || mod == -1 || build > mod {
		t.Fatalf("want Go build cache ranked above Go module cache, got:\n%s", view)
	}
	// Empty findings never render.
	if strings.Contains(view, "Nothing here") {
		t.Fatalf("empty finding should be filtered, got:\n%s", view)
	}
	// Scan errors surface on the row.
	if !strings.Contains(view, "docker: command not found") {
		t.Fatalf("scan error missing, got:\n%s", view)
	}
}

func TestDefaultSelectionAndToggle(t *testing.T) {
	m := newTestModel(t)

	if !m.selected["go-build-cache"] {
		t.Fatal("safe finding should start selected")
	}
	if m.selected["go-mod-cache"] {
		t.Fatal("caution finding must not start selected")
	}
	if m.selected["docker-cache"] {
		t.Fatal("errored finding must not start selected")
	}

	// Cursor starts on the first finding; move to the caution row, toggle.
	m = press(t, m, "j", " ")
	if !m.selected["go-mod-cache"] {
		t.Fatal("space should select the caution finding under the cursor")
	}
	m = press(t, m, " ")
	if m.selected["go-mod-cache"] {
		t.Fatal("space should toggle selection off")
	}
}

func TestSurfaceOnlyNotToggleable(t *testing.T) {
	m := newTestModel(t)
	// Walk to the end: the surface row is the last finding.
	m = press(t, m, "j", "j", "j", "j", " ")
	if m.selected["ios-backups"] {
		t.Fatal("surface-only must never be selectable")
	}
	view := m.View()
	if !strings.Contains(view, "regrow never deletes this") {
		t.Fatalf("cursor note should explain surface-only, got:\n%s", view)
	}
}

func TestPlanScreen(t *testing.T) {
	m := press(t, newTestModel(t), "enter")
	view := m.View()

	if !strings.Contains(view, "nothing executed") {
		t.Fatalf("plan screen must state dry-run, got:\n%s", view)
	}
	if !strings.Contains(view, "go clean -cache") {
		t.Fatalf("plan must show the exact command, got:\n%s", view)
	}
	if strings.Contains(view, "go clean -modcache") {
		t.Fatalf("unselected caution rule must not be planned, got:\n%s", view)
	}
	if !strings.Contains(view, "Would reclaim: 20.0 GiB") {
		t.Fatalf("plan total wrong, got:\n%s", view)
	}

	// esc returns to the checklist.
	m = press(t, m, "esc")
	if m.state != stateList {
		t.Fatalf("esc should return to list, state=%d", m.state)
	}
}

func TestSurfaceOnlySkippedInPlan(t *testing.T) {
	// Select everything selectable, then plan: surface-only must show
	// as a skip, never an action (invariant 5 lives in the planner).
	m := newTestModel(t)
	m.selected["ios-backups"] = true // simulate a bug upstream: planner still refuses
	m = press(t, m, "enter")
	view := m.View()
	if !strings.Contains(view, "surface-only: report, never delete") {
		t.Fatalf("plan must skip surface-only with reason, got:\n%s", view)
	}
	if strings.Contains(view, "MobileSync") {
		t.Fatalf("surface-only path must never appear as an action, got:\n%s", view)
	}
}

func TestCursorNoteShowsRegen(t *testing.T) {
	view := newTestModel(t).View()
	if !strings.Contains(view, "regen: Repopulated by the next go build.") {
		t.Fatalf("cursor note should show regen story, got:\n%s", view)
	}
	if !strings.Contains(view, "cost: next builds slower once") {
		t.Fatalf("cursor note should show regen cost, got:\n%s", view)
	}
}

func TestQuitKeys(t *testing.T) {
	m := newTestModel(t)
	_, cmd := m.Update(key("q"))
	if cmd == nil {
		t.Fatal("q should quit")
	}
	if msg := cmd(); msg != tea.Quit() {
		t.Fatalf("q should produce tea.Quit, got %#v", msg)
	}
}

func TestScanningState(t *testing.T) {
	host := engine.Host{OS: "darwin", Home: "/Users/fixture"}
	m := New(host, "test", func(context.Context) []engine.Finding { return nil })
	if !strings.Contains(m.View(), "scanning") {
		t.Fatalf("initial view should show scanning, got:\n%s", m.View())
	}
	next, _ := m.Update(scanDoneMsg{findings: nil})
	if view := next.(Model).View(); !strings.Contains(view, "Nothing found") {
		t.Fatalf("empty scan should say nothing found, got:\n%s", view)
	}
}
