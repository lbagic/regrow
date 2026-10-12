package scanner

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"unicode/utf16"
)

// plistTexts returns every key and every string value of a property
// list, XML or binary. XML strings come in document order, so a key's
// string value follows it; binary strings come in object-table order,
// which promises nothing. Anything that is not a whole property list
// of one of the two formats is an error.
func plistTexts(data []byte) ([]string, error) {
	if bytes.HasPrefix(data, []byte("bplist")) {
		return binaryPlistStrings(data)
	}
	return xmlPlistStrings(data)
}

func xmlPlistStrings(data []byte) ([]string, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var out []string
	var text []byte
	depth, collecting, isPlist := 0, false, false
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("not a readable property list: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if depth == 0 {
				if t.Name.Local != "plist" {
					return nil, fmt.Errorf("not a property list: root element <%s>", t.Name.Local)
				}
				isPlist = true
			}
			depth++
			collecting = t.Name.Local == "string" || t.Name.Local == "key"
			text = text[:0]
		case xml.CharData:
			if collecting {
				text = append(text, t...)
			}
		case xml.EndElement:
			depth--
			if collecting {
				out = append(out, string(text))
				collecting = false
			}
		}
	}
	if !isPlist {
		return nil, errors.New("not a property list")
	}
	return out, nil
}

// binaryPlistStrings reads the string objects of a bplist00 file
// straight from its object table: the table lists every object, so no
// reference needs following. An object type the format does not
// define is an error, so a string cannot hide behind one.
func binaryPlistStrings(data []byte) ([]string, error) {
	b, err := readBplist(data)
	if err != nil {
		return nil, err
	}
	var out []string
	for i := range b.count {
		at, err := b.objectAt(i)
		if err != nil {
			return nil, err
		}
		s, ok, err := b.stringAt(at)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, s)
		}
	}
	return out, nil
}

// plistTopStrings returns the string values of a property list's
// top-level dictionary, by key, XML or binary. A plist whose top level
// is not a dictionary is an error.
func plistTopStrings(data []byte) (map[string]string, error) {
	if !bytes.HasPrefix(data, []byte("bplist")) {
		return plistStrings(data)
	}
	b, err := readBplist(data)
	if err != nil {
		return nil, err
	}
	at, err := b.objectAt(b.top)
	if err != nil {
		return nil, err
	}
	if data[at]>>4 != 0xd {
		return nil, errBplist("the top object is not a dictionary")
	}
	if b.refSize < 1 || b.refSize > 8 {
		return nil, errBplist("object reference size out of range")
	}
	n, refs, err := b.lengthAt(at)
	if err != nil {
		return nil, err
	}
	if n > (b.tableAt-refs)/(2*b.refSize) {
		return nil, errBplist("dictionary runs past the object table")
	}
	out := map[string]string{}
	for i := range n {
		kref, _ := b.uintAt(refs+i*b.refSize, b.refSize, b.tableAt)
		vref, _ := b.uintAt(refs+(n+i)*b.refSize, b.refSize, b.tableAt)
		kat, err := b.objectAt(kref)
		if err != nil {
			return nil, err
		}
		key, ok, err := b.stringAt(kat)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errBplist("a dictionary key is not a string")
		}
		vat, err := b.objectAt(vref)
		if err != nil {
			return nil, err
		}
		value, ok, err := b.stringAt(vat)
		if err != nil {
			return nil, err
		}
		if ok {
			out[key] = value
		}
	}
	return out, nil
}

func errBplist(what string) error {
	return fmt.Errorf("not a readable binary property list: %s", what)
}

// bplist is the layout of a bplist00 file as its trailer gives it,
// each part checked against the file.
type bplist struct {
	data       []byte
	offsetSize uint64
	refSize    uint64
	count      uint64
	top        uint64
	tableAt    uint64
	body       uint64
}

func readBplist(data []byte) (*bplist, error) {
	const header, trailer = 8, 32
	if len(data) < header+trailer || string(data[:header]) != "bplist00" {
		return nil, errBplist("unknown version or cut short")
	}
	tr := data[len(data)-trailer:]
	b := &bplist{
		data:       data,
		offsetSize: uint64(tr[6]),
		refSize:    uint64(tr[7]),
		count:      binary.BigEndian.Uint64(tr[8:16]),
		top:        binary.BigEndian.Uint64(tr[16:24]),
		tableAt:    binary.BigEndian.Uint64(tr[24:32]),
		body:       uint64(len(data) - trailer),
	}
	if b.offsetSize < 1 || b.offsetSize > 8 || b.tableAt < header || b.tableAt > b.body || b.count > (b.body-b.tableAt)/b.offsetSize {
		return nil, errBplist("offset table out of range")
	}
	return b, nil
}

// uintAt reads a big-endian integer of size bytes at pos; ok is false
// when it would run past limit.
func (b *bplist) uintAt(pos, size, limit uint64) (v uint64, ok bool) {
	if size > 8 || pos > limit || limit-pos < size {
		return 0, false
	}
	for _, c := range b.data[pos : pos+size] {
		v = v<<8 | uint64(c)
	}
	return v, true
}

// objectAt returns where object i starts.
func (b *bplist) objectAt(i uint64) (uint64, error) {
	if i >= b.count {
		return 0, errBplist("object reference out of range")
	}
	at, ok := b.uintAt(b.tableAt+i*b.offsetSize, b.offsetSize, b.body)
	if !ok || at < 8 || at >= b.tableAt {
		return 0, errBplist("object offset out of range")
	}
	return at, nil
}

// lengthAt reads the length of the object at at, from its marker or,
// for 15 or more, from the integer object after it, and returns where
// the object's contents start.
func (b *bplist) lengthAt(at uint64) (n, next uint64, err error) {
	n, next = uint64(b.data[at]&0x0f), at+1
	if n != 0x0f {
		return n, next, nil
	}
	if next >= b.tableAt || b.data[next]>>4 != 0x1 {
		return 0, 0, errBplist("length is not an integer")
	}
	size := uint64(1) << (b.data[next] & 0x0f)
	n, ok := b.uintAt(next+1, size, b.tableAt)
	if !ok {
		return 0, 0, errBplist("length out of range")
	}
	return n, next + 1 + size, nil
}

// stringAt reads the string object at at; ok is false for an object of
// another type the format defines.
func (b *bplist) stringAt(at uint64) (s string, ok bool, err error) {
	kind := b.data[at] >> 4
	switch kind {
	case 0x5, 0x6: // ASCII, UTF-16
	case 0x7, 0x9, 0xe, 0xf:
		return "", false, errBplist("unknown object type")
	default:
		return "", false, nil
	}
	n, at, err := b.lengthAt(at)
	if err != nil {
		return "", false, err
	}
	width := uint64(1)
	if kind == 0x6 {
		width = 2
	}
	if n > (b.tableAt-at)/width {
		return "", false, errBplist("string runs past the object table")
	}
	raw := b.data[at : at+n*width]
	if kind == 0x5 {
		return string(raw), true, nil
	}
	units := make([]uint16, n)
	for j := range units {
		units[j] = binary.BigEndian.Uint16(raw[2*j:])
	}
	return string(utf16.Decode(units)), true, nil
}
