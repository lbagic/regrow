package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// SetRules writes r as the file's `rules:` and keeps the rest of the
// file, its comments included; the `rules:` key itself is written whole.
// Empty rules drop the key, the comments around it kept. The file must
// load as it is, and the new one before it replaces it, which it does in
// one rename; a link is written through to the file it names, and a
// link to no file is refused.
func SetRules(path string, r Rules) error {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	} else if info, lerr := os.Lstat(path); lerr == nil && info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%s is a link to a file that is not there; regrow writes no file through it", path)
	}
	if _, err := LoadFile(path); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	existed := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	// A file of comments alone, or of "---", holds no mapping: its text
	// stays as it is, and the keys follow it.
	var kept []byte
	if isEmptyDocument(&doc) {
		// Past its first "---", a "---" or "..." would put the keys after
		// it in a document of their own: those lines go.
		started := false
		for _, line := range bytes.SplitAfter(data, []byte("\n")) {
			t := string(bytes.TrimSpace(line))
			start, end := marker(t, "---"), marker(t, "...")
			if end || (start && started) {
				continue
			}
			started = started || start
			kept = append(kept, line...)
		}
		kept = bytes.TrimRight(kept, " \t\n")
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	top := doc.Content[0]
	if top.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: the file is not a mapping of keys", path)
	}
	at := -1
	for i := 0; i+1 < len(top.Content); i += 2 {
		if top.Content[i].Value == "rules" {
			at = i
		}
	}
	off := append([]string(nil), r.Off...)
	sort.Strings(off)
	r.Off = off
	switch {
	case len(r.Off) == 0 && !r.Beta && at >= 0:
		dropKey(&doc, top, at)
	case len(r.Off) == 0 && !r.Beta:
	default:
		var value yaml.Node
		if err := value.Encode(r); err != nil {
			return err
		}
		if at >= 0 {
			value.HeadComment, value.LineComment = top.Content[at+1].HeadComment, top.Content[at+1].LineComment
			top.Content[at+1] = &value
		} else {
			top.Content = append(top.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "rules"}, &value)
		}
	}
	// A file written {} once is a flow mapping, which would keep every
	// key after on one line.
	top.Style &^= yaml.FlowStyle
	var out bytes.Buffer
	if len(kept) > 0 {
		out.Write(kept)
		out.WriteByte('\n')
	}
	if len(top.Content) > 0 || top.HeadComment != "" || top.FootComment != "" || doc.HeadComment != "" || doc.FootComment != "" {
		enc := yaml.NewEncoder(&out)
		enc.SetIndent(2)
		if err := enc.Encode(&doc); err != nil {
			return err
		}
		if err := enc.Close(); err != nil {
			return err
		}
	}
	if out.Len() == 0 {
		if !existed {
			return nil
		}
		// A regrow older than the rules key stops on an empty file with a
		// bare EOF.
		out.WriteString("{}\n")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config.yaml.*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	_, werr := tmp.Write(out.Bytes())
	if err := errors.Join(werr, tmp.Sync(), tmp.Close()); err != nil {
		return err
	}
	if _, err := LoadFile(tmp.Name()); err != nil {
		return fmt.Errorf("the new config would not load: %w", err)
	}
	if info, err := os.Stat(path); err == nil {
		if err := os.Chmod(tmp.Name(), info.Mode().Perm()); err != nil {
			return err
		}
	}
	return os.Rename(tmp.Name(), path)
}

// dropKey removes the pair at top.Content[at] and moves the comments
// above and below it to the pair after, or the pair before, or the
// document when none is left.
func dropKey(doc, top *yaml.Node, at int) {
	key, value := top.Content[at], top.Content[at+1]
	var around []string
	for _, c := range []string{key.HeadComment, key.LineComment, value.LineComment, key.FootComment, value.FootComment} {
		if c != "" {
			around = append(around, c)
		}
	}
	top.Content = append(top.Content[:at], top.Content[at+2:]...)
	if len(around) == 0 {
		return
	}
	join := func(a ...string) string {
		var out []string
		for _, s := range a {
			if s != "" {
				out = append(out, s)
			}
		}
		return strings.Join(out, "\n")
	}
	switch {
	case at < len(top.Content):
		next := top.Content[at]
		next.HeadComment = join(append(around, next.HeadComment)...)
	case at > 0:
		prev := top.Content[at-2]
		prev.FootComment = join(append([]string{prev.FootComment}, around...)...)
	default:
		doc.HeadComment = join(append([]string{doc.HeadComment}, around...)...)
	}
}

// marker says whether line is the document marker m, a comment after it
// or not.
func marker(line, m string) bool {
	rest, ok := strings.CutPrefix(line, m)
	return ok && (rest == "" || rest[0] == ' ' || rest[0] == '\t')
}
