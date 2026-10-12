package scanner

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The two fixtures are one launchd job, as XML and as plutil's binary:
// a non-ASCII string (UTF-16 in the binary), one longer than 255 bytes
// (a two-byte length), a dict nested in the dict.
func TestPlistTexts(t *testing.T) {
	var sets [][]string
	for _, name := range []string{"job.xml.plist", "job.bplist"} {
		data, err := os.ReadFile(filepath.Join("testdata", "plist", name))
		if err != nil {
			t.Fatal(err)
		}
		got, err := plistTexts(data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		slices.Sort(got)
		sets = append(sets, got)
		for _, want := range []string{"Label", "dev.example.job", "--flag=café", "PATH", "/Users/dev/.local/share/fnm/node-versions/v22.16.0/installation/bin:/usr/bin:/bin"} {
			if !slices.Contains(got, want) {
				t.Errorf("%s: strings %q lack %q", name, got, want)
			}
		}
		if !slices.ContainsFunc(got, func(s string) bool { return len(s) > 300 && strings.HasPrefix(s, "/opt/tool/") }) {
			t.Errorf("%s: the long argument is missing", name)
		}
	}
	if !slices.Equal(sets[0], sets[1]) {
		t.Errorf("XML and binary read differently:\n%q\n%q", sets[0], sets[1])
	}
}

func TestPlistTextsRefusesWhatIsNotAPlist(t *testing.T) {
	bin, err := os.ReadFile(filepath.Join("testdata", "plist", "job.bplist"))
	if err != nil {
		t.Fatal(err)
	}
	bad := map[string][]byte{
		"not XML":          []byte("PATH=/usr/bin"),
		"another XML root": []byte(`<?xml version="1.0"?><html><string>x</string></html>`),
		"XML cut short":    []byte(`<plist><dict><key>x</key><string>y`),
		"binary cut short": bin[:len(bin)-10],
		"another bplist":   append([]byte("bplist01"), bin[8:]...),
		"a table past the end": func() []byte {
			b := slices.Clone(bin)
			b[len(b)-1] = 0xff
			return b
		}(),
	}
	for name, data := range bad {
		if got, err := plistTexts(data); err == nil {
			t.Errorf("%s: read as %q, want an error", name, got)
		}
	}
}

// onePlistObject is a bplist00 file holding one object, the bytes
// given, as its top object.
func onePlistObject(object []byte) []byte {
	b := append([]byte("bplist00"), object...)
	table := len(b)
	b = append(b, 8)
	trailer := make([]byte, 32)
	trailer[6], trailer[7] = 1, 1 // offset and reference sizes
	trailer[15] = 1               // one object
	trailer[31] = byte(table)
	return append(b, trailer...)
}

// No Apple writer emits type 0x7 in bplist00, and its length unit is
// not settled, so it is refused with the types the format leaves
// undefined.
func TestPlistTextsUnknownObjects(t *testing.T) {
	path := "/Users/dev/.fnm/node-versions/v18.17.1/installation/bin/node"
	if got, err := plistTexts(onePlistObject(append([]byte{0x7f, 0x10, byte(len(path))}, path...))); err == nil {
		t.Errorf("a type 0x7 string read as %q, want an error", got)
	}
	for _, marker := range []byte{0x90, 0xe0, 0xf0} {
		if got, err := plistTexts(onePlistObject([]byte{marker})); err == nil {
			t.Errorf("object type %#x read as %q, want an error", marker, got)
		}
	}
}

// The two fixtures are one Info.plist, as XML and as plutil's binary:
// a non-ASCII value (UTF-16 in the binary), one of 15 bytes or more
// (its length an integer object), an array of dictionaries whose
// strings are not the top level's.
func TestPlistTopStrings(t *testing.T) {
	var maps []map[string]string
	for _, name := range []string{"info.xml.plist", "info.bplist"} {
		data, err := os.ReadFile(filepath.Join("testdata", "plist", name))
		if err != nil {
			t.Fatal(err)
		}
		got, err := plistTopStrings(data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		maps = append(maps, got)
		for key, want := range map[string]string{
			"CFBundleIdentifier":  "dev.example.app",
			"CFBundleDisplayName": "Example Café",
			"CFBundleExecutable":  "Example",
		} {
			if got[key] != want {
				t.Errorf("%s: %s = %q, want %q", name, key, got[key], want)
			}
		}
		if _, ok := got["CFBundleTypeName"]; ok {
			t.Errorf("%s: a nested dictionary's key read as the top level's", name)
		}
		if !strings.HasPrefix(got["NSHumanReadableCopyright"], "A string longer") {
			t.Errorf("%s: the long value is %q", name, got["NSHumanReadableCopyright"])
		}
	}
	if len(maps[0]) != len(maps[1]) {
		t.Errorf("XML and binary read differently:\n%q\n%q", maps[0], maps[1])
	}
	for k, v := range maps[0] {
		if maps[1][k] != v {
			t.Errorf("%s: XML %q, binary %q", k, v, maps[1][k])
		}
	}
}

func TestPlistTopStringsRefusesWhatIsNoDictionary(t *testing.T) {
	bin, err := os.ReadFile(filepath.Join("testdata", "plist", "info.bplist"))
	if err != nil {
		t.Fatal(err)
	}
	bad := map[string][]byte{
		"a string at the top": onePlistObject([]byte{0x51, 'x'}),
		"an empty file":       nil,
		"binary cut short":    bin[:len(bin)-10],
		"no reference size": func() []byte {
			b := slices.Clone(bin)
			b[len(b)-32+7] = 0
			return b
		}(),
		// A one-entry dictionary whose key is the dictionary itself.
		"a key that is no string": onePlistObject([]byte{0xd1, 0x00, 0x00}),
		"an XML array at the top": []byte(`<?xml version="1.0"?><plist><array><string>x</string></array></plist>`),
	}
	for name, data := range bad {
		if got, err := plistTopStrings(data); err == nil {
			t.Errorf("%s: read as %q, want an error", name, got)
		}
	}
}
