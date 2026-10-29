package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// SetRules writes r as the file's `rules:` and keeps the rest of the
// file, its comments included. Empty rules drop the key. The new file
// must load before it replaces the old one, which it does in one
// rename.
func SetRules(path string, r Rules) error {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if doc.Kind == 0 {
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
		top.Content = append(top.Content[:at], top.Content[at+2:]...)
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
	var out bytes.Buffer
	if len(top.Content) > 0 || top.HeadComment != "" || top.FootComment != "" {
		enc := yaml.NewEncoder(&out)
		enc.SetIndent(2)
		if err := enc.Encode(&doc); err != nil {
			return err
		}
		if err := enc.Close(); err != nil {
			return err
		}
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
