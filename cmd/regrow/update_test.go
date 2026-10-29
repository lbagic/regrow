package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/lbagic/regrow/internal/selfupdate"
)

func TestUpdateRefusesADevBuild(t *testing.T) {
	var out bytes.Buffer
	err := runUpdate(&out, nil)
	if err == nil || !strings.Contains(err.Error(), "development build") {
		t.Fatalf("err = %v, want the development build named", err)
	}
	if out.Len() != 0 {
		t.Fatalf("printed %q", out.String())
	}
	if err := runUpdate(&out, []string{"now"}); err == nil {
		t.Fatal("update must refuse arguments")
	}
}

func TestVersionLineIsWhatAnUpdateChecks(t *testing.T) {
	out := stdoutOf(t, func() {
		if err := run([]string{"version"}); err != nil {
			t.Fatal(err)
		}
	})
	if !selfupdate.SaysVersion(out, version) {
		t.Fatalf("regrow version printed %q; an installed release would refuse it", out)
	}
}
