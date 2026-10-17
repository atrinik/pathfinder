package content

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/atrinik/pathfinder/internal/world"
)

// RoadDirectories are the archetype directories whose floors are roads:
// flagstone paving, wooden bridges and snow paths. Long journeys prefer
// them, as a car navigation system prefers highways.
var RoadDirectories = []string{"floors/flagstone/", "floors/bridge_wood/", "floors/snow/snow_path/"}

// IsRoad reports whether an archetype is a road floor.
func IsRoad(a *Archetype) bool {
	if a == nil {
		return false
	}
	for _, dir := range RoadDirectories {
		if strings.HasPrefix(a.Source, dir) {
			return true
		}
	}
	return false
}

// Authored object types the movement model distinguishes.
const (
	typeDoor       = "20"
	typeLockedDoor = "64"
	typeExit       = "66"
)

// Stair "last_heal" values that select the coordinate level above or below.
const (
	stairsUp   = 9
	stairsDown = 10
)

// Load compiles the content checkout at root (holding arch/ and maps/).
func Load(root string) (*world.World, error) {
	archetypes, archFiles, err := loadArchetypes(filepath.Join(root, "arch"))
	if err != nil {
		return nil, err
	}
	maps, mapFiles, err := loadMaps(filepath.Join(root, "maps"))
	if err != nil {
		return nil, err
	}
	digest := sha256.New()
	for _, file := range append(archFiles, mapFiles...) {
		data, err := os.ReadFile(file.abs)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(digest, "%s\x00%d\x00", file.rel, len(data))
		digest.Write(data)
	}
	return Compile(archetypes, maps, hex.EncodeToString(digest.Sum(nil)))
}

type sourceFile struct{ rel, abs string }

func loadArchetypes(dir string) (map[string]*Archetype, []sourceFile, error) {
	archetypes := map[string]*Archetype{}
	var files []sourceFile
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".arc") {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		parsed, err := ParseArchetypes(f)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		rel, _ := filepath.Rel(dir, p)
		for _, a := range parsed {
			a.Source = filepath.ToSlash(rel)
			archetypes[a.Name] = a
		}
		files = append(files, sourceFile{"arch/" + filepath.ToSlash(rel), p})
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })
	return archetypes, files, err
}

func loadMaps(dir string) (map[string]*MapFile, []sourceFile, error) {
	maps := map[string]*MapFile{}
	var files []sourceFile
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(p) != "" {
			// Authored maps have no extension; scripts and data files do.
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		parsed, ok, err := ParseMap(f)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		if !ok {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		key := "/" + filepath.ToSlash(rel)
		maps[key] = parsed
		files = append(files, sourceFile{"maps" + key, p})
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })
	return maps, files, err
}

// Resolve turns an authored destination into an absolute map path.
func Resolve(source, destination string) string {
	if strings.HasPrefix(destination, "/") {
		return path.Clean(destination)
	}
	return path.Clean(path.Join(path.Dir(source), destination))
}

// Coordinate parses a coordinate-map basename such as "world_1_50" or
// "world_6_48_-2": a non-empty prefix followed by exactly two or three
// underscore-separated integers of at most three digits, ending the name.
// It reports ok=false for other names.
func Coordinate(mapPath string) (prefix string, x, y, z int, ok bool) {
	parts := strings.Split(path.Base(mapPath), "_")
	var values []int
	for i := len(parts) - 1; i >= 0; i-- {
		n, numeric := coordinatePart(parts[i])
		if !numeric {
			break
		}
		values = append(values, n)
	}
	count := len(values)
	if count < 2 || count > 3 || count == len(parts) {
		return "", 0, 0, 0, false
	}
	// values holds the trailing integers last-first.
	x, y = values[count-1], values[count-2]
	if count == 3 {
		z = values[0]
	}
	return strings.Join(parts[:len(parts)-count], "_"), x, y, z, true
}

func coordinatePart(part string) (int, bool) {
	digits := strings.TrimPrefix(part, "-")
	if digits == "" || len(digits) > 3 || strings.Trim(digits, "0123456789") != "" {
		return 0, false
	}
	n, err := strconv.Atoi(part)
	return n, err == nil
}

// CoordinateNeighbour names the coordinate map offset from mapPath.
func CoordinateNeighbour(mapPath string, dx, dy, dz int) (string, bool) {
	prefix, x, y, z, ok := Coordinate(mapPath)
	if !ok {
		return "", false
	}
	name := fmt.Sprintf("%s_%d_%d", prefix, x+dx, y+dy)
	if z+dz != 0 {
		name += fmt.Sprintf("_%d", z+dz)
	}
	return path.Join(path.Dir(mapPath), name), true
}

// attr reads an object attribute, falling back to its archetype.
func attr(o *Object, a *Archetype, key string) (string, bool) {
	if value, ok := o.Attrs[key]; ok {
		return value, true
	}
	if a != nil {
		value, ok := a.Attrs[key]
		return value, ok
	}
	return "", false
}

func attrInt(o *Object, a *Archetype, key string, def int) int {
	value, ok := attr(o, a, key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return def
	}
	return n
}

// Compile builds the movement model from parsed archetypes and maps keyed by
// absolute map path.
func Compile(archetypes map[string]*Archetype, files map[string]*MapFile, digest string) (*world.World, error) {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	maps := make([]*world.Map, 0, len(paths))
	for _, p := range paths {
		h := files[p].Header
		width, err := dimension(h, "width")
		if err != nil {
			return nil, fmt.Errorf("map %s: %w", p, err)
		}
		height, err := dimension(h, "height")
		if err != nil {
			return nil, fmt.Errorf("map %s: %w", p, err)
		}
		tiles := int(width) * int(height)
		maps = append(maps, &world.Map{
			Path: p, Name: h["name"], Region: h["region"],
			Width: width, Height: height,
			EnterX: int32(h.Int("enter_x", 0)), EnterY: int32(h.Int("enter_y", 0)),
			Terrain: make([]uint32, tiles), Blocked: make([]bool, tiles), Road: make([]bool, tiles),
			Locks: map[int32]string{}, Exits: map[int32][]world.Exit{},
			Seams: [8]int32{-1, -1, -1, -1, -1, -1, -1, -1},
		})
	}
	w, err := world.New(maps, digest)
	if err != nil {
		return nil, err
	}
	for _, m := range w.Maps {
		file := files[m.Path]
		linkSeams(w, m, file.Header)
		for _, o := range file.Objects {
			if err := placeObject(w, m, file.Header, o, archetypes[o.Arch]); err != nil {
				return nil, fmt.Errorf("map %s: %w", m.Path, err)
			}
		}
	}
	return w, nil
}

// maxDimension bounds a map side; authored maps are far smaller.
const maxDimension = 4096

func dimension(h Attrs, key string) (int32, error) {
	value, ok := h[key]
	if !ok {
		return 0, fmt.Errorf("missing %s", key)
	}
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || n <= 0 || n > maxDimension {
		return 0, fmt.Errorf("invalid %s %q", key, value)
	}
	return int32(n), nil
}

func linkSeams(w *world.World, m *world.Map, header Attrs) {
	for dir := world.North; dir <= world.NorthWest; dir++ {
		if dest := header[fmt.Sprintf("tile_path_%d", dir+1)]; dest != "" {
			if n, ok := w.Lookup(Resolve(m.Path, dest)); ok {
				m.Seams[dir] = n.Index
				continue
			}
		}
		delta := world.Deltas[dir]
		if name, ok := CoordinateNeighbour(m.Path, int(delta[0]), int(delta[1]), 0); ok {
			if n, ok := w.Lookup(name); ok {
				m.Seams[dir] = n.Index
			}
		}
	}
}

// verticalNeighbour finds the map a destination-less stair leads to.
func verticalNeighbour(w *world.World, m *world.Map, header Attrs, level int) (*world.Map, bool) {
	key, dz := "tile_path_9", 1
	if level == stairsDown {
		key, dz = "tile_path_10", -1
	}
	if dest := header[key]; dest != "" {
		if n, ok := w.Lookup(Resolve(m.Path, dest)); ok {
			return n, true
		}
	}
	if name, ok := CoordinateNeighbour(m.Path, 0, 0, dz); ok {
		return w.Lookup(name)
	}
	return nil, false
}

func placeObject(w *world.World, m *world.Map, header Attrs, o *Object, a *Archetype) error {
	x, y := int32(o.Attrs.Int("x", 0)), int32(o.Attrs.Int("y", 0))
	if !m.InBounds(x, y) {
		return nil
	}
	tile := m.Tile(x, y)
	objType, _ := attr(o, a, "type")
	m.Terrain[tile] |= uint32(attrInt(o, a, "terrain_type", 0))
	if IsRoad(a) {
		m.Road[tile] = true
	}
	if blocks(o, a, objType) {
		m.Blocked[tile] = true
	}
	if a != nil {
		// A placed multipart archetype occupies its parts' squares too.
		for _, part := range a.Parts {
			px, py := x+int32(part.DX), y+int32(part.DY)
			if !m.InBounds(px, py) {
				continue
			}
			partTile := m.Tile(px, py)
			partType := part.Attrs["type"]
			if part.Attrs["no_pass"] == "1" && !passableType(partType) {
				m.Blocked[partTile] = true
			}
			m.Terrain[partTile] |= uint32(part.Attrs.Int("terrain_type", 0))
			// A multipart door locks every one of its squares: the part's
			// own slaying, else the placed door's.
			if partType == typeDoor || partType == typeLockedDoor {
				key := part.Attrs["slaying"]
				if key == "" {
					key, _ = attr(o, a, "slaying")
				}
				if key != "" {
					m.Locks[partTile] = key
				}
			}
		}
	}
	switch objType {
	case typeDoor, typeLockedDoor:
		if key, _ := attr(o, a, "slaying"); key != "" {
			m.Locks[tile] = key
		}
	case typeExit:
		return addExit(w, m, header, o, a, tile, x, y)
	}
	return nil
}

func passableType(objType string) bool {
	return objType == typeDoor || objType == typeLockedDoor || objType == typeExit
}

func blocks(o *Object, a *Archetype, objType string) bool {
	value, _ := attr(o, a, "no_pass")
	return value == "1" && !passableType(objType)
}

func addExit(w *world.World, m *world.Map, header Attrs, o *Object, a *Archetype, tile, x, y int32) error {
	walkOn, _ := attr(o, a, "walk_on")
	flyOn, _ := attr(o, a, "fly_on")
	automatic := walkOn == "1" || flyOn == "1"
	label, _ := attr(o, a, "name")
	if label == "" {
		label = o.Arch
	}
	dest, _ := attr(o, a, "slaying")
	if dest != "" {
		n, ok := w.Lookup(Resolve(m.Path, dest))
		if !ok {
			// Exits into maps outside this checkout (or runtime-generated
			// ones) cannot be followed; they are not content errors.
			return nil
		}
		ax, ay := int32(attrInt(o, a, "hp", -1)), int32(attrInt(o, a, "sp", -1))
		if ax < 0 || ay < 0 {
			ax, ay = n.EnterX, n.EnterY
		}
		if !n.InBounds(ax, ay) {
			return fmt.Errorf("exit %q at (%d, %d) arrives at (%d, %d), outside %s (%dx%d)",
				label, x, y, ax, ay, n.Path, n.Width, n.Height)
		}
		m.Exits[tile] = append(m.Exits[tile], world.Exit{
			To: n.Index, X: ax, Y: ay, Automatic: automatic, Label: label,
		})
		return nil
	}
	// Destination-less stairs lead to the same square one level up or down.
	level := attrInt(o, a, "last_heal", 0)
	if level != stairsUp && level != stairsDown {
		return nil
	}
	if n, ok := verticalNeighbour(w, m, header, level); ok && n.InBounds(x, y) {
		m.Exits[tile] = append(m.Exits[tile], world.Exit{
			To: n.Index, X: x, Y: y, Automatic: automatic, Label: label,
		})
	}
	return nil
}
