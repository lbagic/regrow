package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/lbagic/regrow/internal/selfupdate"
)

// startAutoUpdate begins the daily background check for every command
// but `update`; the returned func ends it and says one line on stderr
// when it replaced the binary. It never fails the command.
func startAutoUpdate(args []string) func() {
	if len(args) > 0 && args[0] == "update" {
		return func() {}
	}
	u, err := selfupdate.FromEnv(version)
	if err != nil {
		return func() {}
	}
	r := u.Start(context.Background())
	return func() { r.Finish(os.Stderr, selfupdate.ExitGrace) }
}

func runUpdate(w io.Writer, ids []string) error {
	if len(ids) > 0 {
		return errors.New("update takes no arguments")
	}
	u, err := selfupdate.FromEnv(version)
	if err != nil {
		return err
	}
	res, err := u.Update(context.Background())
	if err != nil {
		return err
	}
	switch res.Status {
	case selfupdate.Updated:
		_, err = fmt.Fprintf(w, "regrow updated from %s to %s; the next run uses it.\n", res.From, res.To)
	case selfupdate.UpToDate:
		_, err = fmt.Fprintf(w, "regrow %s is the latest release.\n", res.From)
	case selfupdate.Ahead:
		_, err = fmt.Fprintf(w, "regrow %s is newer than the latest release, %s; nothing changed.\n", res.From, res.To)
	}
	return err
}
