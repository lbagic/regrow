package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lbagic/regrow/internal/engine"
)

func TestDockerVMMemory(t *testing.T) {
	const store, legacy = "settings-store.json", "settings.json"
	tests := []struct {
		name   string
		files  map[string]string
		locked string
		want   engine.Verdict
		detail []string
	}{
		{
			name: "no Docker Desktop",
			want: engine.VerdictNormal, detail: []string{"does not apply"},
		},
		{
			name:  "settings without a memory key",
			files: map[string]string{store: `{"AutoStart": false, "SettingsVersion": 43}`},
			want:  engine.VerdictFlagged, detail: []string{"no memory limit", "half of this Mac's memory (8.0 GiB of 16.0 GiB)"},
		},
		{
			name:  "limit set",
			files: map[string]string{store: `{"AutoStart": false, "MemoryMiB": 4096}`},
			want:  engine.VerdictNormal, detail: []string{"capped at 4.0 GiB of this Mac's 16.0 GiB"},
		},
		{
			name:  "limit set in the older settings file",
			files: map[string]string{legacy: `{"memoryMiB": 2048, "cpus": 4}`},
			want:  engine.VerdictNormal, detail: []string{"capped at 2.0 GiB"},
		},
		{
			name:  "the newer file wins over a leftover older one",
			files: map[string]string{store: `{"AutoStart": true}`, legacy: `{"memoryMiB": 2048}`},
			want:  engine.VerdictFlagged, detail: []string{"no memory limit"},
		},
		{
			name:  "zero is no limit",
			files: map[string]string{store: `{"MemoryMiB": 0}`},
			want:  engine.VerdictFlagged, detail: []string{"no memory limit"},
		},
		{
			name:  "not JSON",
			files: map[string]string{store: `{"AutoStart": fal`},
			want:  engine.VerdictUnknown, detail: []string{"settings-store.json is not a JSON object"},
		},
		{
			name:  "a memory value that is not a number",
			files: map[string]string{store: `{"MemoryMiB": "lots"}`},
			want:  engine.VerdictUnknown, detail: []string{"MemoryMiB", "not a number"},
		},
		{
			name:   "unreadable",
			files:  map[string]string{store: `{"MemoryMiB": 4096}`},
			locked: store,
			want:   engine.VerdictUnknown, detail: []string{"~/Library/Group Containers/group.com.docker/settings-store.json", "Full Disk Access may be needed"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.locked != "" && os.Geteuid() == 0 {
				t.Skip("root reads mode-000 files")
			}
			c := causeFixture(t)
			dir := filepath.Join(c.host.Home, "Library/Group Containers/group.com.docker")
			for name, text := range tt.files {
				writeText(t, filepath.Join(dir, name), text)
			}
			if tt.locked != "" {
				chmod(t, filepath.Join(dir, tt.locked), 0)
			}
			verdict, detail := c.dockerVMMemory(context.Background(), engine.Rule{})
			wantCause(t, verdict, detail, tt.want, tt.detail...)
		})
	}
}

func TestDockerVMMemoryWithoutAHostFigure(t *testing.T) {
	c := causeFixture(t)
	c.memory = func() int64 { return 0 }
	writeText(t, filepath.Join(c.host.Home, "Library/Group Containers/group.com.docker/settings-store.json"), `{}`)
	verdict, detail := c.dockerVMMemory(context.Background(), engine.Rule{})
	wantCause(t, verdict, detail, engine.VerdictFlagged, "half of this Mac's memory")
	if detail[len(detail)-1] == ')' {
		t.Errorf("detail %q must not print a figure it does not have", detail)
	}
}

func TestDockerBuildCacheLimit(t *testing.T) {
	tests := []struct {
		name   string
		config string
		want   engine.Verdict
		detail []string
	}{
		{
			name:   "Docker Desktop's stock config",
			config: `{"builder": {"gc": {"defaultKeepStorage": "20GB", "enabled": true}}, "experimental": false}`,
			want:   engine.VerdictNormal, detail: []string{"builder.gc.defaultKeepStorage is 20GB"},
		},
		{
			name:   "collection on, sized by the newer key",
			config: `{"builder": {"gc": {"enabled": true, "defaultReservedSpace": "10GB"}}}`,
			want:   engine.VerdictNormal, detail: []string{"builder.gc.defaultReservedSpace is 10GB"},
		},
		{
			name:   "collection on, no size",
			config: `{"builder": {"gc": {"enabled": true}}}`,
			want:   engine.VerdictNormal, detail: []string{"Docker's default limits"},
		},
		{
			name:   "collection on, own policy",
			config: `{"builder": {"gc": {"enabled": true, "policy": [{"keepStorage": "10GB", "all": true}]}}}`,
			want:   engine.VerdictNormal, detail: []string{"builder.gc.policy"},
		},
		{
			name:   "collection switched off, size left behind",
			config: `{"builder": {"gc": {"defaultKeepStorage": "20GB", "enabled": false}}}`,
			want:   engine.VerdictFlagged, detail: []string{"~/.docker/daemon.json switches build-cache garbage collection off"},
		},
		{
			name:   "no builder block",
			config: `{"experimental": false}`,
			want:   engine.VerdictFlagged, detail: []string{"does not switch build-cache garbage collection on"},
		},
		{
			name:   "a size without the switch",
			config: `{"builder": {"gc": {"defaultKeepStorage": "20GB"}}}`,
			want:   engine.VerdictFlagged, detail: []string{"does not switch build-cache garbage collection on"},
		},
		{
			name:   "not JSON",
			config: `builder: gc`,
			want:   engine.VerdictUnknown, detail: []string{"~/.docker/daemon.json is not a JSON object"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := causeFixture(t)
			writeText(t, filepath.Join(c.host.Home, ".docker/daemon.json"), tt.config)
			verdict, detail := c.dockerBuildCacheLimit(context.Background(), engine.Rule{})
			wantCause(t, verdict, detail, tt.want, tt.detail...)
		})
	}

	t.Run("no daemon.json", func(t *testing.T) {
		c := causeFixture(t)
		verdict, detail := c.dockerBuildCacheLimit(context.Background(), engine.Rule{})
		wantCause(t, verdict, detail, engine.VerdictNormal, "does not apply: no ~/.docker/daemon.json")
	})
}
