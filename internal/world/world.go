// Package world holds the compiled, immutable movement model of the content:
// per-map terrain, static blockers, locks, exits, and tiled-map seams.
package world

import "fmt"

// Direction indexes the eight tiled-map neighbours.
type Direction int

// Seam directions, in the authored tile_path_1..8 order.
const (
	North Direction = iota
	East
	South
	West
	NorthEast
	SouthEast
	SouthWest
	NorthWest
)

// Deltas are the unit offsets of each Direction.
var Deltas = [8][2]int32{
	North: {0, -1}, East: {1, 0}, South: {0, 1}, West: {-1, 0},
	NorthEast: {1, -1}, SouthEast: {1, 1}, SouthWest: {-1, 1}, NorthWest: {-1, -1},
}

// TerrainWalk is the authored terrain bit of ordinary walkable ground.
const TerrainWalk uint32 = 1

// Exit is one way off a tile.
type Exit struct {
	To        int32 // destination map index
	X, Y      int32 // arrival tile
	Automatic bool  // stepping onto the tile triggers it
	Label     string
}

// Map is one compiled map. Tile slices are indexed y*Width+x.
type Map struct {
	Index          int32
	Path           string
	Name           string
	Region         string
	Width, Height  int32
	EnterX, EnterY int32
	// Terrain is the union of the terrain bits of every object on the tile;
	// zero means void.
	Terrain []uint32
	// Blocked marks static no_pass squares (walls, furniture, counters).
	Blocked []bool
	// Road marks tiles paved with a road floor.
	Road []bool
	// AutoExit marks tiles holding an automatic exit, so movement checks
	// need not scan a tile's exits. Nil (in hand-built maps) means unknown.
	AutoExit []bool
	// Locks maps a locked door's tile to the key identifier it requires.
	Locks map[int32]string
	// Exits maps a tile to the exits it holds.
	Exits map[int32][]Exit
	// Seams holds the neighbouring map index in each Direction, or -1.
	Seams [8]int32
	// Offset is this map's first global state index.
	Offset int64
}

// Tile returns the index of an in-bounds coordinate.
func (m *Map) Tile(x, y int32) int32 { return y*m.Width + x }

// InBounds reports whether a coordinate lies on the map.
func (m *Map) InBounds(x, y int32) bool {
	return x >= 0 && y >= 0 && x < m.Width && y < m.Height
}

// World is the compiled content.
type World struct {
	Maps   []*Map
	byPath map[string]int32
	// States is the total tile count across maps (global state space size).
	States int64
	// Digest identifies the source files the world was compiled from.
	Digest string
}

// New indexes compiled maps and assigns their global state offsets.
func New(maps []*Map, digest string) (*World, error) {
	w := &World{Maps: maps, byPath: make(map[string]int32, len(maps)), Digest: digest}
	var offset int64
	for i, m := range maps {
		if _, dup := w.byPath[m.Path]; dup {
			return nil, fmt.Errorf("duplicate map %s", m.Path)
		}
		m.Index = int32(i)
		m.Offset = offset
		offset += int64(m.Width) * int64(m.Height)
		w.byPath[m.Path] = int32(i)
	}
	if offset > 1<<31-1 {
		return nil, fmt.Errorf("world has %d tiles; at most 2^31-1 are supported", offset)
	}
	w.States = offset
	return w, nil
}

// Lookup returns the map with the given content path.
func (w *World) Lookup(path string) (*Map, bool) {
	i, ok := w.byPath[path]
	if !ok {
		return nil, false
	}
	return w.Maps[i], true
}

// State returns the global state of a tile.
func (w *World) State(m *Map, x, y int32) int32 {
	return int32(m.Offset) + m.Tile(x, y)
}

// Locate maps a global state back to its map and coordinate.
func (w *World) Locate(state int32) (*Map, int32, int32) {
	lo, hi := 0, len(w.Maps)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if w.Maps[mid].Offset <= int64(state) {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	m := w.Maps[lo]
	tile := int32(int64(state) - m.Offset)
	return m, tile % m.Width, tile / m.Width
}

// Transitions counts every exit and seam in the world.
func (w *World) Transitions() int {
	total := 0
	for _, m := range w.Maps {
		for _, exits := range m.Exits {
			total += len(exits)
		}
		for _, seam := range m.Seams {
			if seam >= 0 {
				total++
			}
		}
	}
	return total
}

// Step resolves one move from (x, y) by (dx, dy), crossing a seam when the
// target lies just outside the map. It returns ok=false when the target is
// off the map with no neighbour there, or outside the neighbour's bounds.
func (w *World) Step(m *Map, x, y, dx, dy int32) (*Map, int32, int32, bool) {
	nx, ny := x+dx, y+dy
	if m.InBounds(nx, ny) {
		return m, nx, ny, true
	}
	var dir Direction
	switch {
	case nx >= m.Width && ny < 0:
		dir = NorthEast
	case nx >= m.Width && ny >= m.Height:
		dir = SouthEast
	case nx < 0 && ny >= m.Height:
		dir = SouthWest
	case nx < 0 && ny < 0:
		dir = NorthWest
	case ny < 0:
		dir = North
	case nx >= m.Width:
		dir = East
	case ny >= m.Height:
		dir = South
	default:
		dir = West
	}
	next := m.Seams[dir]
	if next < 0 {
		return nil, 0, 0, false
	}
	n := w.Maps[next]
	switch {
	case nx < 0:
		nx = n.Width - 1
	case nx >= m.Width:
		nx = 0
	}
	switch {
	case ny < 0:
		ny = n.Height - 1
	case ny >= m.Height:
		ny = 0
	}
	if !n.InBounds(nx, ny) {
		return nil, 0, 0, false
	}
	return n, nx, ny, true
}
