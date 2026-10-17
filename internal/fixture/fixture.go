// Package fixture writes small synthetic content checkouts for tests.
//
// Maps are drawn as text grids. Each character places archetypes:
//
//	.  floor
//	#  floor and wall
//	~  water
//	D  floor and door
//	L  floor and a door locked by "silver_key"
//	(space) void
//
// Exits and other objects are added explicitly with Object.
package fixture

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Archetypes is the fixture archetype file.
const Archetypes = `Object floor
name floor
type 71
terrain_type 1
end
Object wall
name wall
no_pass 1
end
Object water
name water
terrain_type 2
end
Object door
name door
type 20
no_pass 1
end
Object locked_door
name locked door
type 20
no_pass 1
slaying silver_key
end
Object portal
name portal
type 66
end
Object teleporter
name teleporter
type 66
walk_on 1
end
Object stairs_up
name stairs going up
type 66
last_heal 9
hp -1
sp -1
end
Object stairs_down
name stairs going down
type 66
last_heal 10
hp -1
sp -1
end
Object long_wall
name long wall
no_pass 1
msg
A wall two squares wide.
endmsg
end
More
Object long_wall_b
name long wall
no_pass 1
x 1
end
`

var glyphs = map[rune][]string{
	'.': {"floor"},
	'#': {"floor", "wall"},
	'~': {"water"},
	'D': {"floor", "door"},
	'L': {"floor", "locked_door"},
}

// Content is a fixture checkout under construction.
type Content struct {
	t    testing.TB
	Root string
	maps map[string]*Map
}

// Map is one fixture map.
type Map struct {
	Path    string
	Header  map[string]string
	Rows    []string
	objects []string
}

// New starts a fixture checkout in a temporary directory.
func New(t testing.TB) *Content {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "arch"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "arch", "fixture.arc"), []byte(Archetypes), 0o644); err != nil {
		t.Fatal(err)
	}
	return &Content{t: t, Root: root, maps: map[string]*Map{}}
}

// Map adds a map drawn from rows; all rows must have the same width.
func (c *Content) Map(path string, rows ...string) *Map {
	m := &Map{Path: path, Header: map[string]string{
		"width":  fmt.Sprint(len(rows[0])),
		"height": fmt.Sprint(len(rows)),
		"name":   filepath.Base(path),
	}, Rows: rows}
	c.maps[path] = m
	return m
}

// Set sets a header attribute.
func (m *Map) Set(key, value string) *Map {
	m.Header[key] = value
	return m
}

// Object places an archetype at (x, y) with extra attribute lines.
func (m *Map) Object(arch string, x, y int, attrs ...string) *Map {
	var b strings.Builder
	fmt.Fprintf(&b, "arch %s\nx %d\ny %d\n", arch, x, y)
	for _, line := range attrs {
		b.WriteString(line + "\n")
	}
	b.WriteString("end\n")
	m.objects = append(m.objects, b.String())
	return m
}

// Write writes every map and returns the checkout root.
func (c *Content) Write() string {
	c.t.Helper()
	for _, m := range c.maps {
		var b strings.Builder
		b.WriteString("arch map\n")
		for key, value := range m.Header {
			fmt.Fprintf(&b, "%s %s\n", key, value)
		}
		b.WriteString("end\n")
		for y, row := range m.Rows {
			for x, ch := range row {
				for _, arch := range glyphs[ch] {
					fmt.Fprintf(&b, "arch %s\nx %d\ny %d\nend\n", arch, x, y)
				}
			}
		}
		for _, object := range m.objects {
			b.WriteString(object)
		}
		file := filepath.Join(c.Root, "maps", filepath.FromSlash(strings.TrimPrefix(m.Path, "/")))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			c.t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(b.String()), 0o644); err != nil {
			c.t.Fatal(err)
		}
	}
	return c.Root
}
