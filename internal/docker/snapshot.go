package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Exec runs the docker CLI and returns stdout. found is false only
// when the binary is not on PATH. Stdout is returned even when the
// command exits non-zero: batch `inspect` prints every object it did
// find and errors only for the missing ones.
type Exec func(ctx context.Context, args ...string) (out []byte, found bool, err error)

// realExec is the production Exec.
func realExec(ctx context.Context, args ...string) ([]byte, bool, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return nil, false, nil
	}
	out, err := exec.CommandContext(ctx, "docker", args...).Output()
	return out, true, err
}

// Volume is one docker volume with everything the classifier needs.
type Volume struct {
	Name      string
	CreatedAt time.Time
	Labels    map[string]string
	Bytes     int64
	// UsedBy lists the names of containers (running or stopped) whose
	// mounts reference the volume. Non-empty means protected.
	UsedBy []string
	// LastUsed is the derived timestamp: the newest StartedAt or
	// FinishedAt across referencing containers, merged with the usage
	// ledger. Zero when nothing was ever observed.
	LastUsed time.Time
}

// Anonymous reports whether docker created the volume implicitly (the
// label is authoritative; anonymous volumes also get 64-hex names).
func (v Volume) Anonymous() bool {
	_, ok := v.Labels["com.docker.volume.anonymous"]
	return ok
}

// Project is the compose project the volume belongs to, if any.
func (v Volume) Project() string {
	return v.Labels["com.docker.compose.project"]
}

// Container is one container, running or stopped.
type Container struct {
	ID         string
	Name       string
	Image      string
	Status     string // created, running, paused, restarting, removing, exited, dead
	Created    time.Time
	StartedAt  time.Time
	FinishedAt time.Time
	// VolumeNames are the names of volume-type mounts.
	VolumeNames []string
	Labels      map[string]string
	// Bytes is the writable layer (what `docker rm` frees).
	Bytes int64
}

// Stopped reports whether the container is not alive in any form.
func (c Container) Stopped() bool {
	switch c.Status {
	case "created", "exited", "dead":
		return true
	}
	return false
}

// LastUsed is the container's own recency: the newest of its
// lifecycle timestamps.
func (c Container) LastUsed() time.Time {
	t := c.Created
	if c.StartedAt.After(t) {
		t = c.StartedAt
	}
	if c.FinishedAt.After(t) {
		t = c.FinishedAt
	}
	return t
}

// Project is the compose project, if any.
func (c Container) Project() string {
	return c.Labels["com.docker.compose.project"]
}

// Image is one image as `system df -v` reports it.
type Image struct {
	ID         string
	Repository string
	Tag        string
	CreatedAt  time.Time
	// Bytes is the unique size — what deleting this image alone frees.
	Bytes int64
	// Containers is the number of containers using the image.
	Containers int
	// LastTagTime is the local pull/tag time (fetched for dangling
	// candidates only); zero when never tagged or not fetched.
	LastTagTime time.Time
}

// Dangling reports an untagged image.
func (i Image) Dangling() bool {
	return i.Repository == "<none>" || i.Repository == ""
}

// BuildCache is one buildkit cache record — the only docker object
// with a native last-used.
type BuildCache struct {
	ID         string
	Bytes      int64
	InUse      bool
	Shared     bool
	LastUsedAt time.Time
}

// Snapshot is one scan-time view of the daemon.
type Snapshot struct {
	Volumes    []Volume
	Containers []Container
	Images     []Image
	BuildCache []BuildCache
}

// dfJSON mirrors `docker system df -v --format {{json .}}`. Numbers
// and booleans arrive as strings; sizes are human-formatted (SI).
type dfJSON struct {
	Images []struct {
		ID         string `json:"ID"`
		Repository string `json:"Repository"`
		Tag        string `json:"Tag"`
		CreatedAt  string `json:"CreatedAt"`
		UniqueSize string `json:"UniqueSize"`
		Containers string `json:"Containers"`
	} `json:"Images"`
	Containers []struct {
		ID   string `json:"ID"`
		Size string `json:"Size"`
	} `json:"Containers"`
	Volumes []struct {
		Name  string `json:"Name"`
		Links string `json:"Links"`
		Size  string `json:"Size"`
	} `json:"Volumes"`
	BuildCache []struct {
		ID         string `json:"ID"`
		Size       string `json:"Size"`
		InUse      string `json:"InUse"`
		Shared     string `json:"Shared"`
		LastUsedAt string `json:"LastUsedAt"`
	} `json:"BuildCache"`
}

// containerInspectFormat trims `container inspect` to what the join
// needs — never the full config, which drags env vars (secrets) into
// memory and test fixtures.
const containerInspectFormat = `{"ID":{{json .Id}},"Name":{{json .Name}},"Created":{{json .Created}},"Status":{{json .State.Status}},"StartedAt":{{json .State.StartedAt}},"FinishedAt":{{json .State.FinishedAt}},"Mounts":{{json .Mounts}},"Labels":{{json .Config.Labels}},"Image":{{json .Config.Image}}}`

type containerJSON struct {
	ID         string `json:"ID"`
	Name       string `json:"Name"`
	Created    string `json:"Created"`
	Status     string `json:"Status"`
	StartedAt  string `json:"StartedAt"`
	FinishedAt string `json:"FinishedAt"`
	Mounts     []struct {
		Type string `json:"Type"`
		Name string `json:"Name"`
	} `json:"Mounts"`
	Labels map[string]string `json:"Labels"`
	Image  string            `json:"Image"`
}

type volumeJSON struct {
	Name      string            `json:"Name"`
	CreatedAt string            `json:"CreatedAt"`
	Labels    map[string]string `json:"Labels"`
}

const imageInspectFormat = `{"ID":{{json .Id}},"LastTagTime":{{json .Metadata.LastTagTime}}}`

type imageMetaJSON struct {
	ID          string `json:"ID"`
	LastTagTime string `json:"LastTagTime"`
}

// loadSnapshot enumerates the daemon. (nil, nil) means docker is not
// available right now — binary missing or daemon down — so the rules
// simply do not apply. Any failure after the probe succeeds is a real
// error: a half-visible daemon must not classify volumes as dangling.
func loadSnapshot(ctx context.Context, run Exec) (*Snapshot, error) {
	out, found, err := run(ctx, "system", "df", "-v", "--format", "{{json .}}")
	if !found || err != nil {
		return nil, nil
	}
	var df dfJSON
	if err := json.Unmarshal(out, &df); err != nil {
		return nil, fmt.Errorf("docker system df: %w", err)
	}

	snap := &Snapshot{}

	containerBytes := map[string]int64{}
	ids := make([]string, 0, len(df.Containers))
	for _, c := range df.Containers {
		ids = append(ids, c.ID)
		containerBytes[c.ID], _ = parseSize(c.Size)
	}
	if len(ids) > 0 {
		args := append([]string{"container", "inspect", "--format", containerInspectFormat}, ids...)
		out, _, err := run(ctx, args...)
		containers, perr := parseContainerLines(out)
		// A vanished container is fine (inspect prints the rest); a
		// call that produced nothing while containers exist is not.
		if perr != nil || (err != nil && len(containers) == 0) {
			return nil, fmt.Errorf("docker container inspect: %w", firstErr(err, perr))
		}
		for i := range containers {
			containers[i].Bytes = containerBytes[containers[i].ID]
		}
		snap.Containers = containers
	}

	volumeBytes := map[string]int64{}
	volumeLinks := map[string]int{}
	names := make([]string, 0, len(df.Volumes))
	for _, v := range df.Volumes {
		names = append(names, v.Name)
		volumeBytes[v.Name], _ = parseSize(v.Size)
		volumeLinks[v.Name], _ = strconv.Atoi(v.Links)
	}
	if len(names) > 0 {
		args := append([]string{"volume", "inspect"}, names...)
		out, _, err := run(ctx, args...)
		var vols []volumeJSON
		if perr := json.Unmarshal(out, &vols); perr != nil {
			return nil, fmt.Errorf("docker volume inspect: %w", firstErr(err, perr))
		}
		for _, v := range vols {
			snap.Volumes = append(snap.Volumes, Volume{
				Name:      v.Name,
				CreatedAt: parseDockerTime(v.CreatedAt),
				Labels:    v.Labels,
				Bytes:     volumeBytes[v.Name],
			})
		}
	}

	for _, im := range df.Images {
		n, _ := strconv.Atoi(im.Containers)
		bytes, _ := parseSize(im.UniqueSize)
		snap.Images = append(snap.Images, Image{
			ID:         im.ID,
			Repository: im.Repository,
			Tag:        im.Tag,
			CreatedAt:  parseDockerTime(im.CreatedAt),
			Bytes:      bytes,
			Containers: n,
		})
	}
	if err := fetchImageMeta(ctx, run, snap); err != nil {
		return nil, err
	}

	for _, bc := range df.BuildCache {
		bytes, _ := parseSize(bc.Size)
		inUse, _ := strconv.ParseBool(bc.InUse)
		shared, _ := strconv.ParseBool(bc.Shared)
		snap.BuildCache = append(snap.BuildCache, BuildCache{
			ID:         bc.ID,
			Bytes:      bytes,
			InUse:      inUse,
			Shared:     shared,
			LastUsedAt: parseDockerTime(bc.LastUsedAt),
		})
	}

	joinVolumeUsage(snap, volumeLinks)
	return snap, nil
}

// fetchImageMeta pulls LastTagTime for the dangling, unreferenced
// images — the only ones a rule will offer — so their items carry an
// honest recency signal.
func fetchImageMeta(ctx context.Context, run Exec, snap *Snapshot) error {
	var ids []string
	for _, im := range snap.Images {
		if im.Dangling() && im.Containers == 0 {
			ids = append(ids, im.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	args := append([]string{"image", "inspect", "--format", imageInspectFormat}, ids...)
	out, _, err := run(ctx, args...)
	lastTag := map[string]time.Time{}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var m imageMetaJSON
		if perr := json.Unmarshal([]byte(line), &m); perr != nil {
			return fmt.Errorf("docker image inspect: %w", firstErr(err, perr))
		}
		lastTag[m.ID] = parseDockerTime(m.LastTagTime)
	}
	if err != nil && len(lastTag) == 0 {
		return fmt.Errorf("docker image inspect: %w", err)
	}
	for i := range snap.Images {
		snap.Images[i].LastTagTime = lastTag[snap.Images[i].ID]
	}
	return nil
}

// joinVolumeUsage derives each volume's references and last-used from
// the containers that mount it (research §2). The df link count backs
// up the join: if inspect missed a referencing container, Links>0
// still marks the volume as used — misclassifying protected data as
// dangling is the one direction this must never fail in.
func joinVolumeUsage(snap *Snapshot, links map[string]int) {
	byName := map[string]*Volume{}
	for i := range snap.Volumes {
		byName[snap.Volumes[i].Name] = &snap.Volumes[i]
	}
	for _, c := range snap.Containers {
		for _, name := range c.VolumeNames {
			v, ok := byName[name]
			if !ok {
				continue
			}
			v.UsedBy = append(v.UsedBy, c.Name)
			if t := c.LastUsed(); t.After(v.LastUsed) {
				v.LastUsed = t
			}
		}
	}
	for i := range snap.Volumes {
		v := &snap.Volumes[i]
		sort.Strings(v.UsedBy)
		if len(v.UsedBy) == 0 && links[v.Name] > 0 {
			v.UsedBy = []string{"(unknown container)"}
		}
	}
}

func parseContainerLines(out []byte) ([]Container, error) {
	var containers []Container
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var c containerJSON
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, err
		}
		var volNames []string
		for _, m := range c.Mounts {
			if m.Type == "volume" && m.Name != "" {
				volNames = append(volNames, m.Name)
			}
		}
		containers = append(containers, Container{
			ID:          c.ID,
			Name:        strings.TrimPrefix(c.Name, "/"),
			Image:       c.Image,
			Status:      c.Status,
			Created:     parseDockerTime(c.Created),
			StartedAt:   parseDockerTime(c.StartedAt),
			FinishedAt:  parseDockerTime(c.FinishedAt),
			VolumeNames: volNames,
			Labels:      c.Labels,
		})
	}
	return containers, nil
}

// parseDockerTime handles the two formats the CLI emits: RFC3339
// (inspect) and Go's time.String() (`system df -v`). Docker's zero
// timestamps ("0001-01-01T00:00:00Z") parse to time.Time zero, which
// is exactly what LastUsed-zero means. Unparseable input is treated
// as unknown, never as an error — a timestamp is a recency signal,
// not a contract.
func parseDockerTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05 -0700 MST"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// parseSize parses docker's human sizes ("1.5GB (50%)", "0B"). Docker
// uses SI units: kB = 1000 bytes.
func parseSize(s string) (int64, bool) {
	if before, _, found := strings.Cut(s, " "); found {
		s = before
	}
	i := strings.IndexFunc(s, func(r rune) bool {
		return r != '.' && (r < '0' || r > '9')
	})
	if i <= 0 {
		return 0, false
	}
	val, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0, false
	}
	mult, ok := map[string]float64{
		"B": 1, "kB": 1e3, "KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12,
	}[s[i:]]
	if !ok {
		return 0, false
	}
	return int64(val * mult), true
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
