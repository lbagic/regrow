// Package tui implements the interactive checklist from PRODUCT.md §4:
// findings grouped by category, size-ranked, risk-colored, the regen
// story shown for the row under the cursor, and a plan screen (the
// exact commands) before anything would run. Nothing here executes —
// the plan screen is the contract the Phase 2 executor fulfils.
package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/lbagic/regrow/internal/engine"
)

type state int

const (
	stateScanning state = iota
	stateList
	statePlan
)

// Plain ANSI palette so the user's terminal theme applies. Risk colors
// are the visual half of ARCHITECTURE.md invariant 5.
var (
	styleTitle   = lipgloss.NewStyle().Bold(true)
	styleFaint   = lipgloss.NewStyle().Faint(true)
	styleSafe    = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styleCaution = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styleSurface = lipgloss.NewStyle().Foreground(lipgloss.Color("4"))
	styleErr     = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
)

func riskStyle(r engine.Risk) lipgloss.Style {
	switch r {
	case engine.RiskSafe:
		return styleSafe
	case engine.RiskCaution:
		return styleCaution
	default:
		return styleSurface
	}
}

// riskLabel is the short tag rendered next to a row.
func riskLabel(r engine.Risk) string {
	if r == engine.RiskSurfaceOnly {
		return "surface"
	}
	return string(r)
}

type rowKind int

const (
	rowHeader rowKind = iota
	rowFinding
)

// row is one rendered line of the checklist: a category header or a
// finding. The cursor only ever rests on findings.
type row struct {
	kind     rowKind
	category string
	bytes    int64 // header: category total
	finding  int   // index into model.findings for rowFinding
}

type scanDoneMsg struct{ findings []engine.Finding }

type tickMsg time.Time

func tick() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Model is the Bubble Tea model for the whole interactive flow:
// scanning → checklist → plan.
type Model struct {
	host    engine.Host
	version string
	scan    func(context.Context) []engine.Finding

	state    state
	frame    int
	findings []engine.Finding
	rows     []row
	cursor   int // index into rows; always a rowFinding
	selected map[string]bool
	plan     engine.Plan

	width  int
	height int
}

// New builds the model. scan is injected so tests drive the UI against
// fixture findings without touching the disk.
func New(host engine.Host, version string, scan func(context.Context) []engine.Finding) Model {
	return Model{
		host:     host,
		version:  version,
		scan:     scan,
		selected: map[string]bool{},
		width:    80,
		height:   24,
	}
}

// Run starts the interactive UI; the scan runs in the background so
// the screen is responsive immediately.
func Run(host engine.Host, version string, scan func(context.Context) []engine.Finding) error {
	_, err := tea.NewProgram(New(host, version, scan), tea.WithAltScreen()).Run()
	return err
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		func() tea.Msg { return scanDoneMsg{findings: m.scan(context.Background())} },
		tick(),
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tickMsg:
		if m.state != stateScanning {
			return m, nil
		}
		m.frame++
		return m, tick()

	case scanDoneMsg:
		m.findings = msg.findings
		m.rows = buildRows(m.findings)
		m.cursor = firstFinding(m.rows)
		// Safe findings start selected (PRODUCT.md pillar 2: auto-clean
		// class); caution needs a human tick; surface-only never.
		for _, f := range m.findings {
			if f.Rule.Risk == engine.RiskSafe && len(f.Items) > 0 && f.Err == "" {
				m.selected[f.Rule.ID] = true
			}
		}
		m.state = stateList
		return m, nil

	case tea.KeyMsg:
		return m.updateKeys(msg)
	}
	return m, nil
}

func (m Model) updateKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit

	case "esc":
		if m.state == statePlan {
			m.state = stateList
		}
		return m, nil

	case "up", "k":
		if m.state == stateList {
			m.cursor = nextFinding(m.rows, m.cursor, -1)
		}
		return m, nil

	case "down", "j":
		if m.state == stateList {
			m.cursor = nextFinding(m.rows, m.cursor, +1)
		}
		return m, nil

	case " ":
		if m.state != stateList {
			return m, nil
		}
		if f := m.currentFinding(); f != nil && toggleable(*f) {
			if m.selected[f.Rule.ID] {
				delete(m.selected, f.Rule.ID)
			} else {
				m.selected[f.Rule.ID] = true
			}
		}
		return m, nil

	case "enter":
		if m.state != stateList {
			return m, nil
		}
		m.plan = engine.BuildPlan(m.host, m.findings, m.selected)
		m.state = statePlan
		return m, nil
	}
	return m, nil
}

func (m Model) currentFinding() *engine.Finding {
	if m.cursor < 0 || m.cursor >= len(m.rows) || m.rows[m.cursor].kind != rowFinding {
		return nil
	}
	return &m.findings[m.rows[m.cursor].finding]
}

// toggleable: surface-only is never selectable (invariant 5), and a
// row with nothing to reclaim has nothing to toggle.
func toggleable(f engine.Finding) bool {
	return f.Rule.Risk.Actionable() && len(f.Items) > 0
}

// buildRows filters findings to those with substance (items or a scan
// error), groups by category, and size-ranks both levels.
func buildRows(findings []engine.Finding) []row {
	byCategory := map[string][]int{}
	for i, f := range findings {
		if len(f.Items) == 0 && f.Err == "" {
			continue
		}
		byCategory[f.Rule.Category] = append(byCategory[f.Rule.Category], i)
	}

	totals := map[string]int64{}
	for c, idxs := range byCategory {
		for _, i := range idxs {
			totals[c] += findings[i].TotalBytes()
		}
	}
	categories := make([]string, 0, len(byCategory))
	for c := range byCategory {
		categories = append(categories, c)
	}
	sort.Slice(categories, func(i, j int) bool {
		if totals[categories[i]] != totals[categories[j]] {
			return totals[categories[i]] > totals[categories[j]]
		}
		return categories[i] < categories[j]
	})

	var rows []row
	for _, c := range categories {
		idxs := byCategory[c]
		sort.Slice(idxs, func(i, j int) bool {
			bi, bj := findings[idxs[i]].TotalBytes(), findings[idxs[j]].TotalBytes()
			if bi != bj {
				return bi > bj
			}
			return findings[idxs[i]].Rule.ID < findings[idxs[j]].Rule.ID
		})
		rows = append(rows, row{kind: rowHeader, category: c, bytes: totals[c]})
		for _, i := range idxs {
			rows = append(rows, row{kind: rowFinding, category: c, finding: i})
		}
	}
	return rows
}

func firstFinding(rows []row) int {
	for i, r := range rows {
		if r.kind == rowFinding {
			return i
		}
	}
	return -1
}

// nextFinding moves the cursor over finding rows only, skipping
// headers, clamped at both ends.
func nextFinding(rows []row, from, dir int) int {
	for i := from + dir; i >= 0 && i < len(rows); i += dir {
		if rows[i].kind == rowFinding {
			return i
		}
	}
	return from
}

func (m Model) View() string {
	switch m.state {
	case stateScanning:
		return m.viewScanning()
	case statePlan:
		return m.viewPlan()
	default:
		return m.viewList()
	}
}

func (m Model) header() string {
	return styleTitle.Render("regrow "+m.version) +
		styleFaint.Render("  scan → inventory, grouped, size-ranked") + "\n" +
		styleFaint.Render(strings.Repeat("─", min(m.width, 72))) + "\n"
}

func (m Model) viewScanning() string {
	frame := spinnerFrames[m.frame%len(spinnerFrames)]
	return m.header() + fmt.Sprintf("\n  %s scanning — measuring every rule, nothing is deleted\n", frame)
}

func (m Model) viewList() string {
	if firstFinding(m.rows) == -1 {
		return m.header() + "\n  Nothing found: no rule matched anything on this machine.\n\n" +
			styleFaint.Render("  q quit") + "\n"
	}

	var lines []string
	cursorLine := 0
	for i, r := range m.rows {
		if r.kind == rowHeader {
			label := strings.ToUpper(strings.ReplaceAll(r.category, "-", " "))
			lines = append(lines, styleTitle.Render(fmt.Sprintf("  %-42s %10s", label, HumanBytes(r.bytes))))
			continue
		}
		if i == m.cursor {
			cursorLine = len(lines)
		}
		lines = append(lines, m.findingLine(m.findings[r.finding], i == m.cursor))
	}

	body := m.window(lines, cursorLine)
	return m.header() + strings.Join(body, "\n") + "\n" + m.listFooter()
}

// findingLine renders one checklist row:
//
//	› [x] Go build cache             20.1 GiB  safe     go clean -cache
func (m Model) findingLine(f engine.Finding, atCursor bool) string {
	marker := " "
	if atCursor {
		marker = "›"
	}

	box := "   "
	switch {
	case f.Rule.Risk == engine.RiskSurfaceOnly:
		box = " ▸ "
	case toggleable(f):
		if m.selected[f.Rule.ID] {
			box = "[x]"
		} else {
			box = "[ ]"
		}
	}

	if f.Err != "" && len(f.Items) == 0 {
		return styleErr.Render(fmt.Sprintf("%s !   %-30s scan failed: %s", marker, f.Rule.Title, f.Err))
	}

	note := ShellJoin(f.Rule.NativeCommand)
	if note == "" {
		note = f.Rule.Regen.Story
	}
	if n := len(f.Items); n > 1 {
		note = fmt.Sprintf("%d locations · %s", n, note)
	}

	title := f.Rule.Title
	if atCursor {
		title = styleTitle.Render(title)
	}
	return fmt.Sprintf("%s %s %-30s %10s  %s  %s",
		marker, box, title,
		HumanBytes(f.TotalBytes()),
		riskStyle(f.Rule.Risk).Render(fmt.Sprintf("%-8s", riskLabel(f.Rule.Risk))),
		styleFaint.Render(truncate(note, m.width-62)),
	)
}

// listFooter: the note for the row under the cursor, then keys.
func (m Model) listFooter() string {
	var note string
	if f := m.currentFinding(); f != nil {
		switch {
		case f.Err != "":
			note = "error: " + f.Err
		case f.Rule.Risk == engine.RiskSurfaceOnly:
			note = "surface-only: shown so you know it exists — regrow never deletes this"
		default:
			note = fmt.Sprintf("regen: %s · cost: %s", f.Rule.Regen.Story, f.Rule.Regen.Cost)
			if d := daysUnused(*f); d > 0 {
				note += fmt.Sprintf(" · unused %dd", d)
			}
			if f.Rule.Note != "" {
				note = f.Rule.Note + " · " + note
			}
		}
	}

	var selCount int
	var selBytes int64
	for _, f := range m.findings {
		if m.selected[f.Rule.ID] {
			selCount++
			selBytes += f.TotalBytes()
		}
	}

	return styleFaint.Render(strings.Repeat("─", min(m.width, 72))) + "\n" +
		styleFaint.Render(truncate("  "+note, m.width)) + "\n" +
		fmt.Sprintf("  selected %d · ~%s   ", selCount, HumanBytes(selBytes)) +
		styleFaint.Render("space toggle · enter plan · ↑↓ move · q quit") + "\n"
}

// daysUnused: most recent LastUsed across items, in whole days ago.
func daysUnused(f engine.Finding) int {
	var last time.Time
	for _, it := range f.Items {
		if it.LastUsed.After(last) {
			last = it.LastUsed
		}
	}
	if last.IsZero() {
		return 0
	}
	return int(time.Since(last).Hours() / 24)
}

// viewPlan is the screen every run ends on before anything could
// execute: the exact commands, trash destinations, skips with reasons.
func (m Model) viewPlan() string {
	var b strings.Builder
	b.WriteString(m.header())
	b.WriteString(styleTitle.Render("  PLAN — dry run, nothing executed") + "\n\n")

	if len(m.plan.Actions) == 0 && len(m.plan.Skipped) == 0 {
		b.WriteString("  Nothing selected found anything to reclaim.\n")
	}
	for _, a := range m.plan.Actions {
		b.WriteString(fmt.Sprintf("  [%s] %-24s %10s  %s\n",
			a.Kind, a.RuleID, HumanBytes(a.Bytes), ShellJoin(a.Command)))
	}
	for _, s := range m.plan.Skipped {
		b.WriteString(styleFaint.Render(fmt.Sprintf("  [skip]   %-22s %s", s.RuleID, s.Reason)) + "\n")
	}

	b.WriteString(fmt.Sprintf("\n  Would reclaim: %s\n", styleTitle.Render(HumanBytes(m.plan.TotalBytes()))))
	b.WriteString(styleFaint.Render("  Execute with `regrow clean [id ...]` — trash first, `regrow undo` restores.") + "\n\n")
	b.WriteString(styleFaint.Render("  esc back · q quit") + "\n")
	return b.String()
}

// window scrolls the body so the cursor line stays visible.
func (m Model) window(lines []string, cursorLine int) []string {
	const chrome = 6 // header (2) + footer (3) + margin
	bodyH := m.height - chrome
	if bodyH < 1 {
		bodyH = 1
	}
	if len(lines) <= bodyH {
		return lines
	}
	start := 0
	if cursorLine >= bodyH {
		start = cursorLine - bodyH + 1
	}
	end := start + bodyH
	if end > len(lines) {
		end = len(lines)
	}
	return lines[start:end]
}

func truncate(s string, w int) string {
	if w < 4 {
		w = 4
	}
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	return string(r[:w-1]) + "…"
}
