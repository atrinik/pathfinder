// Package atlas builds and searches the world's road network.
//
// The atlas is a graph of connectors: the tiles where travel leaves or
// enters a walkable area (exit tiles, exit arrivals and seam crossings).
// Within one walkable component of a map, every pair of connectors is joined
// by a walk edge whose cost comes from a road-weighted search over that map,
// so routes between connectors follow roads where they can; exits and seams
// join connectors across maps. A plan searches this small graph instead of
// every tile, then expands only the legs it uses into click-sized waypoints.
//
// The atlas is built for a plain walker (walking terrain, no keys, strict
// diagonal corners). Requests for other agents use exact path searches.
package atlas

import (
	"context"
	"runtime"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/atrinik/pathfinder/internal/world"
)

// ConnectorKind flags what a connector tile is for.
type ConnectorKind uint8

// Connector kinds; one tile may be several at once.
const (
	// AppliedExit: an exit used by standing on the tile and applying it.
	AppliedExit ConnectorKind = 1 << iota
	// AutomaticExit: an exit triggered by stepping onto the tile.
	AutomaticExit
	// Arrival: where an exit lands.
	Arrival
	// Crossing: a border tile a seam is crossed from or onto.
	Crossing
)

// Connector is one atlas node.
type Connector struct {
	Map       int32
	X, Y      int32
	Kind      ConnectorKind
	Component int32 // the walker component it stands in, -1 for automatic exits
}

// EdgeKind is how an edge moves.
type EdgeKind uint8

// Edge kinds.
const (
	WalkEdge EdgeKind = iota
	ExitEdge
	SeamEdge
)

// Edge is one directed atlas edge.
type Edge struct {
	To        int32
	Cost      int32
	Kind      EdgeKind
	Automatic bool
	Label     string
}

// Atlas is the built road network of one world.
type Atlas struct {
	World      *world.World
	Connectors []Connector
	Out        [][]Edge
	// Components labels each map's walker components per tile (-1: none).
	Components [][]int32
	// byState maps a connector's global tile state to its index.
	byState map[int32]int32
	// componentConnectors lists, per map and component, the connectors that
	// can be walked to from that component (its own, and adjacent automatic
	// exits); mapConnectors lists those of every component of a map, in id
	// order.
	componentConnectors []map[int32][]int32
	mapConnectors       [][]int32
	stats               Stats
	// BuildTime is how long building (or loading) the atlas took.
	BuildTime time.Duration
	// Loaded is true when the atlas came from an atlas file.
	Loaded  bool
	pending []transition // transitions found while adding connectors
}

// Stats summarizes an atlas.
type Stats struct {
	Connectors, Edges, RoadTiles int
}

// Stats returns the atlas' counts, computed once when it was built or
// loaded.
func (a *Atlas) Stats() Stats { return a.stats }

func (a *Atlas) count() Stats {
	s := Stats{Connectors: len(a.Connectors)}
	for _, out := range a.Out {
		s.Edges += len(out)
	}
	for _, m := range a.World.Maps {
		for _, road := range m.Road {
			if road {
				s.RoadTiles++
			}
		}
	}
	return s
}

// Lookup returns the connector at a tile.
func (a *Atlas) Lookup(m *world.Map, x, y int32) (int32, bool) {
	id, ok := a.byState[a.World.State(m, x, y)]
	return id, ok
}

// Build computes the atlas of w. Per-map work runs in parallel.
func Build(w *world.World) *Atlas {
	started := time.Now()
	a := newAtlas(w)
	a.addConnectors()
	a.Out = make([][]Edge, len(a.Connectors))
	a.addTransitions()
	a.groupConnectors()
	a.addWalks()
	a.stats = a.count()
	a.BuildTime = time.Since(started)
	return a
}

// newAtlas starts an atlas of w with its walker components labelled.
func newAtlas(w *world.World) *Atlas {
	a := &Atlas{
		World:               w,
		Components:          make([][]int32, len(w.Maps)),
		byState:             map[int32]int32{},
		componentConnectors: make([]map[int32][]int32, len(w.Maps)),
		mapConnectors:       make([][]int32, len(w.Maps)),
	}
	parallel(len(w.Maps), func(i int) {
		a.Components[i], _ = components(w.Maps[i])
	})
	return a
}

func parallel(n int, work func(int)) {
	var wg sync.WaitGroup
	next := make(chan int)
	for range runtime.GOMAXPROCS(0) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				work(i)
			}
		}()
	}
	for i := range n {
		next <- i
	}
	close(next)
	wg.Wait()
}

// connector returns the connector at a tile, creating it with kind.
func (a *Atlas) connector(m *world.Map, x, y int32, kind ConnectorKind) int32 {
	state := a.World.State(m, x, y)
	if id, ok := a.byState[state]; ok {
		a.Connectors[id].Kind |= kind
		return id
	}
	id := int32(len(a.Connectors))
	a.Connectors = append(a.Connectors, Connector{
		Map: m.Index, X: x, Y: y, Kind: kind, Component: a.Components[m.Index][m.Tile(x, y)],
	})
	a.byState[state] = id
	return id
}

// transition is a cross-map (or same-map exit) move between two connectors.
type transition struct {
	from, to  int32
	kind      EdgeKind
	automatic bool
	label     string
}

func (a *Atlas) addConnectors() {
	w := a.World
	for _, m := range w.Maps {
		tiles := make([]int32, 0, len(m.Exits))
		for tile := range m.Exits {
			tiles = append(tiles, tile)
		}
		sort.Slice(tiles, func(i, j int) bool { return tiles[i] < tiles[j] })
		for _, tile := range tiles {
			x, y := tile%m.Width, tile/m.Width
			for _, exit := range m.Exits[tile] {
				dest := w.Maps[exit.To]
				if !dest.InBounds(exit.X, exit.Y) {
					continue
				}
				kind := AppliedExit
				if exit.Automatic {
					kind = AutomaticExit
				} else if isAutomaticExitTile(m, tile) {
					continue // being on the tile takes its automatic exit instead
				}
				// An exit on a tile nobody can enter stays: an agent starting
				// there can still use it.
				arrivalTile := dest.Tile(exit.X, exit.Y)
				arrivalKind := Arrival
				if !standable(dest, arrivalTile) {
					if !passable(dest, arrivalTile) || !isAutomaticExitTile(dest, arrivalTile) {
						continue // lands somewhere a walker cannot be
					}
					arrivalKind |= AutomaticExit
				}
				from := a.connector(m, x, y, kind)
				to := a.connector(dest, exit.X, exit.Y, arrivalKind)
				a.pending = append(a.pending, transition{
					from: from, to: to, kind: ExitEdge, automatic: exit.Automatic, label: exit.Label,
				})
			}
		}
		a.addSeamConnectors(m)
	}
}

func isAutomaticExitTile(m *world.Map, tile int32) bool {
	for _, exit := range m.Exits[tile] {
		if exit.Automatic {
			return true
		}
	}
	return false
}

// addSeamConnectors adds one crossing per run of border tiles that lead
// from the same component here to the same component on the neighbour.
func (a *Atlas) addSeamConnectors(m *world.Map) {
	w := a.World
	for dir := world.North; dir <= world.NorthWest; dir++ {
		if m.Seams[dir] < 0 {
			continue
		}
		d := world.Deltas[dir]
		type pair struct{ x, y, nx, ny, from, to int32 }
		var runs [][]pair
		var current []pair
		var key [2]int32
		flush := func() {
			if len(current) > 0 {
				runs = append(runs, current)
			}
			current = nil
		}
		for _, b := range borderTiles(m, dir) {
			tile := m.Tile(b[0], b[1])
			n, nx, ny, ok := w.Step(m, b[0], b[1], d[0], d[1])
			if !ok || !standable(m, tile) || !a.cornersOpen(m, b[0], b[1], d) {
				flush()
				continue
			}
			if dest := n.Tile(nx, ny); !standable(n, dest) {
				flush()
				if passable(n, dest) && isAutomaticExitTile(n, dest) {
					// Stepping across onto an automatic exit takes it at
					// once; each such tile leads somewhere of its own.
					runs = append(runs, []pair{{b[0], b[1], nx, ny, a.Components[m.Index][tile], -1}})
				}
				continue
			}
			k := [2]int32{a.Components[m.Index][tile], a.Components[n.Index][n.Tile(nx, ny)]}
			if len(current) > 0 && k != key {
				flush()
			}
			key = k
			current = append(current, pair{b[0], b[1], nx, ny, k[0], k[1]})
		}
		flush()
		n := w.Maps[m.Seams[dir]]
		for _, run := range runs {
			mid := run[len(run)/2]
			from := a.connector(m, mid.x, mid.y, Crossing)
			to := a.connector(n, mid.nx, mid.ny, Crossing)
			a.pending = append(a.pending, transition{from: from, to: to, kind: SeamEdge})
		}
	}
}

// cornersOpen reports whether a strict-corner walker may take the step d
// from (x, y): a diagonal needs both orthogonal neighbours, on whichever map
// they lie, passable.
func (a *Atlas) cornersOpen(m *world.Map, x, y int32, d [2]int32) bool {
	if d[0] == 0 || d[1] == 0 {
		return true
	}
	for _, step := range [2][2]int32{{d[0], 0}, {0, d[1]}} {
		om, ox, oy, ok := a.World.Step(m, x, y, step[0], step[1])
		if !ok || !passable(om, om.Tile(ox, oy)) {
			return false
		}
	}
	return true
}

// borderTiles lists the tiles of m on the side facing dir, in order.
func borderTiles(m *world.Map, dir world.Direction) [][2]int32 {
	var tiles [][2]int32
	switch dir {
	case world.North:
		for x := int32(0); x < m.Width; x++ {
			tiles = append(tiles, [2]int32{x, 0})
		}
	case world.South:
		for x := int32(0); x < m.Width; x++ {
			tiles = append(tiles, [2]int32{x, m.Height - 1})
		}
	case world.West:
		for y := int32(0); y < m.Height; y++ {
			tiles = append(tiles, [2]int32{0, y})
		}
	case world.East:
		for y := int32(0); y < m.Height; y++ {
			tiles = append(tiles, [2]int32{m.Width - 1, y})
		}
	case world.NorthEast:
		tiles = append(tiles, [2]int32{m.Width - 1, 0})
	case world.SouthEast:
		tiles = append(tiles, [2]int32{m.Width - 1, m.Height - 1})
	case world.SouthWest:
		tiles = append(tiles, [2]int32{0, m.Height - 1})
	case world.NorthWest:
		tiles = append(tiles, [2]int32{0, 0})
	}
	return tiles
}

func (a *Atlas) addTransitions() {
	for _, t := range a.pending {
		a.Out[t.from] = append(a.Out[t.from], Edge{
			To: t.to, Cost: Transition, Kind: t.kind, Automatic: t.automatic, Label: t.label,
		})
	}
	a.pending = nil
}

// groupConnectors lists, per map component, the connectors walkable to from
// it.
func (a *Atlas) groupConnectors() {
	w := a.World
	perMap := make([][]int32, len(w.Maps))
	for id, c := range a.Connectors {
		perMap[c.Map] = append(perMap[c.Map], int32(id))
	}
	parallel(len(w.Maps), func(i int) {
		m := w.Maps[i]
		groups := map[int32][]int32{}
		for _, id := range perMap[i] {
			c := a.Connectors[id]
			if c.Component >= 0 {
				groups[c.Component] = append(groups[c.Component], id)
				continue
			}
			if c.Kind&AutomaticExit == 0 || !passable(m, m.Tile(c.X, c.Y)) {
				continue // an exit nobody can enter: only for a start there
			}
			// An automatic exit is reached by stepping onto it from any
			// adjacent component.
			seen := map[int32]bool{}
			open := func(t int32) bool { return t == m.Tile(c.X, c.Y) || standable(m, t) }
			neighbours(m, m.Tile(c.X, c.Y), open, func(n int32) {
				if comp := a.Components[i][n]; comp >= 0 && !seen[comp] {
					seen[comp] = true
					groups[comp] = append(groups[comp], id)
				}
			})
		}
		a.componentConnectors[i] = groups
		seen := map[int32]bool{}
		for _, members := range groups {
			for _, id := range members {
				if !seen[id] {
					seen[id] = true
					a.mapConnectors[i] = append(a.mapConnectors[i], id)
				}
			}
		}
		slices.Sort(a.mapConnectors[i])
	})
}

// addWalks joins the connectors walkable to from each component.
func (a *Atlas) addWalks() {
	w := a.World
	walks := make([][]walkEdge, len(w.Maps))
	parallel(len(w.Maps), func(i int) {
		m := w.Maps[i]
		var g gridSearch
		for _, members := range a.componentConnectors[i] {
			for _, from := range members {
				fc := a.Connectors[from]
				if fc.Component < 0 {
					continue // nobody walks away from an automatic exit
				}
				_ = g.run(context.Background(), m, m.Tile(fc.X, fc.Y), nil)
				for _, to := range members {
					if to == from {
						continue
					}
					tc := a.Connectors[to]
					tile := m.Tile(tc.X, tc.Y)
					cost := g.cost(tile)
					if tc.Component < 0 {
						var ok bool
						if cost, _, ok = g.stepOnto(m, tile); !ok {
							continue
						}
					}
					if cost >= 0 {
						walks[i] = append(walks[i], walkEdge{from, to, cost})
					}
				}
			}
		}
	})
	for _, edges := range walks {
		// Component groups come out of a map in random order; sort so equal
		// content always yields the same atlas.
		sort.Slice(edges, func(i, j int) bool {
			if edges[i].from != edges[j].from {
				return edges[i].from < edges[j].from
			}
			return edges[i].to < edges[j].to
		})
		for _, e := range edges {
			a.Out[e.from] = append(a.Out[e.from], Edge{To: e.to, Cost: e.cost, Kind: WalkEdge})
		}
	}
}

type walkEdge struct{ from, to, cost int32 }
