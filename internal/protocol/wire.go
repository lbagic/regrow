package protocol

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/executor"
	"github.com/lbagic/regrow/internal/headroom"
	"github.com/lbagic/regrow/internal/oplog"
)

// Version is the protocol version hello announces.
const Version = 1

// MaxLine is the longest request line accepted, newline excluded.
const MaxLine = 1 << 20

const (
	reqScan    = "scan"
	reqPlan    = "plan"
	reqExecute = "execute"
	reqCancel  = "cancel"
	reqTick    = "tick"
	reqHistory = "history"
	reqRules   = "rules"
	reqSetRule = "set_rules"
)

// Error codes, the `code` of an error event.
const (
	CodeBadRequest     = "bad_request"
	CodeLineTooLong    = "line_too_long"
	CodeUnknownRequest = "unknown_request"
	CodeBusy           = "busy"
	CodeUnknownScan    = "unknown_scan"
	CodeScanRunning    = "scan_running"
	CodeScanCanceled   = "scan_canceled"
	CodeScanSuperseded = "scan_superseded"
	CodeScanSpent      = "scan_spent"
	CodeUnmatched      = "unmatched"
	CodeTickFailed     = "tick_failed"
	CodeAutotrimLocked = "autotrim_locked"
	CodeUnknownPlan    = "unknown_plan"
	CodePlanExpired    = "plan_expired"
	CodeExecuteFailed  = "execute_failed"
	CodeHistoryFailed  = "history_failed"
	CodeRulesFailed    = "rules_failed"
)

type request struct {
	Type   string `json:"type"`
	ID     string `json:"id"`
	ScanID string `json:"scan_id"`
	// Select nil (absent or null) plans the default selection; an
	// empty list plans nothing.
	Select   *[]string `json:"select"`
	PlanID   string    `json:"plan_id"`
	Target   string    `json:"target"`
	Autotrim bool      `json:"autotrim"`
	// Rule and Off turn one rule off or on; Beta turns the beta rules
	// on or off. A set_rules request carries one or the other.
	Rule string `json:"rule"`
	Off  *bool  `json:"off"`
	Beta *bool  `json:"beta"`
}

// head opens every event: its name and the id of the request it
// answers (empty for hello and for errors about an unparsed line).
type head struct {
	Event string `json:"event"`
	Re    string `json:"re,omitempty"`
}

type helloEvent struct {
	head
	Protocol int    `json:"protocol"`
	Version  string `json:"version"`
	FDA      FDA    `json:"fda"`
	Self     string `json:"self,omitempty"`
}

// inlineEvent puts the fields of body, a JSON object, on the event
// line beside event and re.
type inlineEvent struct {
	head
	body any
}

func (e inlineEvent) MarshalJSON() ([]byte, error) {
	h, err := json.Marshal(e.head)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(e.body)
	if err != nil {
		return nil, err
	}
	if len(b) < 2 || b[0] != '{' {
		return nil, fmt.Errorf("%s event body is not a JSON object: %.40s", e.Event, b)
	}
	if string(b) == "{}" {
		return h, nil
	}
	return append(append(h[:len(h)-1], ','), b[1:]...), nil
}

type startEvent struct {
	head
	ScanID string `json:"scan_id"`
	Rules  int    `json:"rules"`
}

type findingEvent struct {
	head
	ScanID string `json:"scan_id"`
	Index  int    `json:"index"`
	TookMS int64  `json:"took_ms"`
	// Bucket is the total the rule's bytes are counted in.
	Bucket string `json:"bucket"`
	// Select is the atoms that tick the row, absent when it cannot be
	// ticked: a peer sends only atoms it was given here.
	Select  []string       `json:"select,omitempty"`
	Finding engine.Finding `json:"finding"`
}

type summaryEvent struct {
	head
	ScanID    string           `json:"scan_id"`
	Totals    engine.Totals    `json:"totals"`
	Exclusive map[string]int64 `json:"exclusive"`
}

type planEvent struct {
	head
	PlanID string `json:"plan_id"`
	// ExpiresAt is the wall-clock moment after which an execute of the
	// plan answers plan_expired.
	ExpiresAt time.Time `json:"expires_at"`
	// Select is the atoms the plan was built from, sorted: the
	// request's, or the default selection when it sent none.
	Select []string      `json:"select"`
	Plan   engine.Plan   `json:"plan"`
	Totals engine.Totals `json:"totals"`
}

type journalEvent struct {
	head
	Entry oplog.Entry `json:"entry"`
}

// History is what a history request answers: every run in the oplog,
// oldest first, and the free-space samples of the headroom history.
type History struct {
	Runs    []oplog.Summary   `json:"runs"`
	Samples []headroom.Sample `json:"samples"`
	// SamplesUnreadable: the headroom history could not be read, and
	// Samples is empty.
	SamplesUnreadable bool `json:"samples_unreadable,omitempty"`
	// Titles names each rule the runs counted bytes for that the
	// engine's catalog still holds, by rule id.
	Titles map[string]string `json:"titles"`
}

// ReadHistory reads a history request's answer. A journal that cannot
// be read fails it, since the journal is never read past a bad line; a
// headroom history that cannot be read is only said so.
func ReadHistory(oplogPath, headroomPath string) (History, error) {
	entries, err := oplog.Read(oplogPath)
	if err != nil {
		return History{}, err
	}
	var h History
	for _, r := range oplog.Runs(entries) {
		h.Runs = append(h.Runs, r.Summary())
	}
	h.Samples, err = headroom.Tail(headroomPath, time.Time{})
	h.SamplesUnreadable = err != nil
	if err != nil {
		h.Samples = nil
	}
	return h, nil
}

// Rules is the engine's catalog and which of it runs, as config.yaml's
// `rules:` and the engine's --beta-rules leave it.
type Rules struct {
	// Rules is every rule of the catalog, beta ones included. Its name
	// on the wire is catalog: a scan's start event counts its rules in
	// a field named rules.
	Rules []RuleState `json:"catalog"`
	// Beta is config.yaml's `rules: beta`; BetaFlag says the engine was
	// started with --beta-rules, which runs them whatever Beta says.
	Beta     bool `json:"beta"`
	BetaFlag bool `json:"beta_flag"`
	// Config is the file's path.
	Config string `json:"config"`
	// Error says the file no longer loads: the rules as it last loaded
	// stand until it does.
	Error string `json:"error,omitempty"`
}

// RuleState is one rule, whether config.yaml turns it off, and whether
// it runs: not off, and not beta while the beta rules are off.
type RuleState struct {
	engine.Rule
	Off  bool `json:"off"`
	Runs bool `json:"runs"`
}

// RuleSwitch is the engine's say over which rules run.
type RuleSwitch interface {
	// Catalog is what a scan or tick starting now runs.
	Catalog() []engine.Rule
	State() Rules
	// Set turns one rule off or on (rule and off), or the beta rules
	// (beta), in config.yaml.
	Set(rule string, off, beta *bool) (Rules, error)
}

type rulesEvent struct {
	head
	Rules
}

type historyEvent struct {
	head
	History
}

type doneEvent struct {
	head
}

type scanDoneEvent struct {
	head
	ElapsedMS int64 `json:"elapsed_ms"`
	Canceled  bool  `json:"canceled"`
}

type executeDoneEvent struct {
	head
	Result   executor.Result `json:"result"`
	Canceled bool            `json:"canceled"`
	// StagingDir is the run's folder in the staging directory.
	StagingDir string `json:"staging_dir,omitempty"`
}

type errorEvent struct {
	head
	Code    string `json:"code"`
	Message string `json:"message"`
	// Unmatched lists the selectors of an unmatched plan request.
	Unmatched []string `json:"unmatched,omitempty"`
	// Result is what a failed execute did before it failed.
	Result *executor.Result `json:"result,omitempty"`
}

func done(re string) doneEvent { return doneEvent{head{"done", re}} }

func failure(re, code, message string) errorEvent {
	return errorEvent{head: head{"error", re}, Code: code, Message: message}
}
