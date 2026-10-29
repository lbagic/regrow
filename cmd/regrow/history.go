package main

import (
	"path/filepath"

	"github.com/lbagic/regrow/internal/headroom"
	"github.com/lbagic/regrow/internal/oplog"
	"github.com/lbagic/regrow/internal/protocol"
)

// engineHistory answers the engine's history request from the oplog and
// the headroom history beside it in regrow's state directory.
func engineHistory() (protocol.History, error) {
	logPath, err := oplog.DefaultPath()
	if err != nil {
		return protocol.History{}, err
	}
	return protocol.ReadHistory(logPath, filepath.Join(filepath.Dir(logPath), headroom.FileName))
}
