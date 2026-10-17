// Package content reads the authored Atrinik archetype and map text formats.
//
// Both formats are line-based blocks of "key value" attributes. Archetype
// files hold "Object NAME" ... "end" blocks, with "More" introducing the next
// part of a multipart archetype. Map files start with an "arch map" header
// block followed by "arch NAME" ... "end" object blocks; a block nested
// inside an object is that object's inventory. Multi-line "msg" ... "endmsg"
// text is skipped.
package content

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Attrs are an object's authored attributes.
type Attrs map[string]string

// Int returns an integer attribute, or def when it is absent or malformed.
func (a Attrs) Int(key string, def int) int {
	value, ok := a[key]
	if !ok {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return def
	}
	return n
}

// Object is one placed map object and its inventory.
type Object struct {
	Arch     string
	Attrs    Attrs
	Children []*Object
}

// Part is one extra square of a multipart archetype, relative to its head.
type Part struct {
	DX, DY int
	Attrs  Attrs
}

// Archetype is a named object template; map objects override its attributes.
type Archetype struct {
	Name  string
	Attrs Attrs
	Parts []Part
}

// MapFile is one parsed map.
type MapFile struct {
	Header  Attrs
	Objects []*Object
}

type lineReader struct {
	scanner *bufio.Scanner
	line    int
}

func newLineReader(r io.Reader) *lineReader {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	return &lineReader{scanner: scanner}
}

// next returns the next non-empty line, trimmed of trailing whitespace.
func (l *lineReader) next() (string, bool, error) {
	for l.scanner.Scan() {
		l.line++
		text := strings.TrimRight(l.scanner.Text(), " \t\r")
		if strings.TrimSpace(text) == "" {
			continue
		}
		return text, true, nil
	}
	return "", false, l.scanner.Err()
}

// skipText consumes lines up to and including the terminator.
func (l *lineReader) skipText(terminator string) error {
	for {
		text, ok, err := l.next()
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("line %d: missing %q", l.line, terminator)
		}
		if strings.TrimSpace(text) == terminator {
			return nil
		}
	}
}

func splitAttr(text string) (string, string) {
	key, value, _ := strings.Cut(strings.TrimSpace(text), " ")
	return key, strings.TrimSpace(value)
}

// ParseArchetypes reads every archetype in one .arc file.
func ParseArchetypes(r io.Reader) ([]*Archetype, error) {
	lines := newLineReader(r)
	var archetypes []*Archetype
	var head *Archetype
	more := false
	for {
		text, ok, err := lines.next()
		if err != nil {
			return nil, err
		}
		if !ok {
			return archetypes, nil
		}
		key, value := splitAttr(text)
		switch key {
		case "More":
			if head == nil {
				return nil, fmt.Errorf("line %d: More without a preceding Object", lines.line)
			}
			more = true
			continue
		case "Object":
			attrs, err := readArchetypeAttrs(lines)
			if err != nil {
				return nil, err
			}
			if more {
				head.Parts = append(head.Parts, Part{
					DX: attrs.Int("x", 0), DY: attrs.Int("y", 0), Attrs: attrs,
				})
				more = false
				continue
			}
			head = &Archetype{Name: value, Attrs: attrs}
			archetypes = append(archetypes, head)
		default:
			// Animation and other top-level blocks are not archetypes.
			if key == "anim" {
				if err := lines.skipText("mina"); err != nil {
					return nil, err
				}
			}
		}
	}
}

func readArchetypeAttrs(lines *lineReader) (Attrs, error) {
	attrs := Attrs{}
	for {
		text, ok, err := lines.next()
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("line %d: Object missing end", lines.line)
		}
		key, value := splitAttr(text)
		switch key {
		case "end":
			return attrs, nil
		case "msg":
			if err := lines.skipText("endmsg"); err != nil {
				return nil, err
			}
		case "anim":
			if err := lines.skipText("mina"); err != nil {
				return nil, err
			}
		default:
			attrs[key] = value
		}
	}
}

// ParseMap reads one map file. It reports ok=false for files that are not
// maps (they do not start with an "arch map" header).
func ParseMap(r io.Reader) (*MapFile, bool, error) {
	lines := newLineReader(r)
	text, ok, err := lines.next()
	if err != nil || !ok {
		return nil, false, err
	}
	if key, value := splitAttr(text); key != "arch" || value != "map" {
		return nil, false, nil
	}
	header, _, err := readMapObject(lines, "map")
	if err != nil {
		return nil, true, err
	}
	file := &MapFile{Header: header.Attrs}
	for {
		text, ok, err := lines.next()
		if err != nil {
			return nil, true, err
		}
		if !ok {
			return file, true, nil
		}
		key, value := splitAttr(text)
		if key != "arch" {
			return nil, true, fmt.Errorf("line %d: expected arch, got %q", lines.line, text)
		}
		object, _, err := readMapObject(lines, value)
		if err != nil {
			return nil, true, err
		}
		file.Objects = append(file.Objects, object)
	}
}

func readMapObject(lines *lineReader, arch string) (*Object, bool, error) {
	object := &Object{Arch: arch, Attrs: Attrs{}}
	for {
		text, ok, err := lines.next()
		if err != nil {
			return nil, false, err
		}
		if !ok {
			return nil, false, fmt.Errorf("line %d: arch %s missing end", lines.line, arch)
		}
		key, value := splitAttr(text)
		switch key {
		case "end":
			return object, true, nil
		case "arch":
			child, _, err := readMapObject(lines, value)
			if err != nil {
				return nil, false, err
			}
			object.Children = append(object.Children, child)
		case "msg":
			if err := lines.skipText("endmsg"); err != nil {
				return nil, false, err
			}
		default:
			object.Attrs[key] = value
		}
	}
}
