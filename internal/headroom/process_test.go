package headroom

import (
	"reflect"
	"testing"
)

const psOutput = `    1  21264 /sbin/launchd
  412 8388608 /Applications/Virtual Box.app/Contents/MacOS/Virtual Machine Service
 9001 524288 /usr/local/go/pkg/tool/darwin_arm64/compile
 9002  10240 /usr/local/go/bin/go
 9003   4096 gopls
garbage line
`

func TestParsePS(t *testing.T) {
	ps := parsePS([]byte(psOutput))
	want := []Process{
		{PID: 1, Name: "launchd", RSS: 21264 << 10},
		{PID: 412, Name: "Virtual Machine Service", RSS: 8 * GiB},
		{PID: 9001, Name: "compile", RSS: 524288 << 10},
		{PID: 9002, Name: "go", RSS: 10240 << 10},
		{PID: 9003, Name: "gopls", RSS: 4096 << 10},
	}
	if !reflect.DeepEqual(ps, want) {
		t.Fatalf("parsePS = %+v\nwant %+v", ps, want)
	}
	top, ok := Top(ps)
	if !ok || top.PID != 412 {
		t.Errorf("Top = %+v, %v; want pid 412", top, ok)
	}
	if _, ok := Top(nil); ok {
		t.Error("Top of no processes must report none")
	}
	// gopls is not go: names match whole.
	if got := Running(ps, []string{"link", "go", "compile"}); !reflect.DeepEqual(got, []string{"compile", "go"}) {
		t.Errorf("Running = %v, want [compile go]", got)
	}
	if got := Running(ps, []string{"link"}); got != nil {
		t.Errorf("Running = %v, want none", got)
	}
}

func TestParsePurgeable(t *testing.T) {
	got, err := parsePurgeable([]byte(`{"important":60000000000,"available":35000000000}`))
	if err != nil || got != 25000000000 {
		t.Errorf("parsePurgeable = %d, %v; want 25000000000", got, err)
	}
	// Finder's number can lag under df's for a moment; never negative.
	if got, err := parsePurgeable([]byte(`{"important":10,"available":20}`)); err != nil || got != 0 {
		t.Errorf("parsePurgeable = %d, %v; want 0", got, err)
	}
	if _, err := parsePurgeable([]byte(`execution error`)); err == nil {
		t.Error("unparseable probe output must be an error")
	}
}
