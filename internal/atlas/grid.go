package atlas

import (
	"context"

	"github.com/atrinik/pathfinder/internal/world"
)

// Step costs, in thirds of a move: a step between two road tiles costs
// RoadStep, any other step PlainStep, and a transition (an exit or a seam
// crossing) Transition. Road travel is therefore a third cheaper, so long
// journeys follow roads the way a car navigation system follows highways.
const (
	RoadStep   = 2
	PlainStep  = 3
	Transition = 3
)

// passable reports whether a walker (plain walking terrain, no keys) can
// move through a tile: walkable, not blocked and not locked. Diagonal steps
// need both corner tiles passable.
func passable(m *world.Map, tile int32) bool {
	if m.Terrain[tile]&world.TerrainWalk == 0 || m.Blocked[tile] {
		return false
	}
	_, locked := m.Locks[tile]
	return !locked
}

// standable reports whether a walker can stop on a tile: passable and not an
// automatic exit (stepping onto one carries the walker away).
func standable(m *world.Map, tile int32) bool {
	if !passable(m, tile) {
		return false
	}
	for _, exit := range m.Exits[tile] {
		if exit.Automatic {
			return false
		}
	}
	return true
}

// stepCost is the cost of moving between two adjacent tiles.
func stepCost(m *world.Map, from, to int32) int32 {
	if m.Road[from] && m.Road[to] {
		return RoadStep
	}
	return PlainStep
}

// neighbours calls visit for each open tile one strict-corner step from tile
// within the map. Corners need only be passable: a diagonal step past an
// automatic exit does not touch it.
func neighbours(m *world.Map, tile int32, open func(int32) bool, visit func(int32)) {
	x, y := tile%m.Width, tile/m.Width
	for _, d := range world.Deltas {
		nx, ny := x+d[0], y+d[1]
		if !m.InBounds(nx, ny) {
			continue
		}
		next := m.Tile(nx, ny)
		if !open(next) {
			continue
		}
		if d[0] != 0 && d[1] != 0 && (!passable(m, m.Tile(x+d[0], y)) || !passable(m, m.Tile(x, y+d[1]))) {
			continue
		}
		visit(next)
	}
}

// components labels the walker components of a map: tiles connected by
// strict-corner steps share an id; other tiles are -1.
func components(m *world.Map) ([]int32, int32) {
	labels := make([]int32, len(m.Terrain))
	for i := range labels {
		labels[i] = -1
	}
	open := func(t int32) bool { return standable(m, t) }
	var next int32
	var queue []int32
	for tile := range labels {
		t := int32(tile)
		if labels[t] >= 0 || !open(t) {
			continue
		}
		labels[t] = next
		queue = append(queue[:0], t)
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			neighbours(m, cur, open, func(n int32) {
				if labels[n] < 0 {
					labels[n] = next
					queue = append(queue, n)
				}
			})
		}
		next++
	}
	return labels, next
}

// gridSearch is a reusable Dijkstra over one map's standable tiles.
type gridSearch struct {
	dist   []int32
	parent []int32
	stamp  []uint32
	gen    uint32
	open   tileHeap
}

const unreached = int32(-1)

func (g *gridSearch) prepare(tiles int) {
	if len(g.dist) < tiles {
		g.dist = make([]int32, tiles)
		g.parent = make([]int32, tiles)
		g.stamp = make([]uint32, tiles)
		g.gen = 0
	}
	g.gen++
	if g.gen == 0 {
		clear(g.stamp)
		g.gen = 1
	}
	g.open = g.open[:0]
}

func (g *gridSearch) cost(tile int32) int32 {
	if g.stamp[tile] != g.gen {
		return unreached
	}
	return g.dist[tile]
}

// cancelCheckInterval is how many tiles a search settles between checks of
// its context.
const cancelCheckInterval = 1024

// run computes walker costs from source to every standable tile of its
// component. The source itself need not be standable (an agent can start
// on a door or exit tile). It stops with the context's error when the
// context ends.
func (g *gridSearch) run(ctx context.Context, m *world.Map, source int32) error {
	g.prepare(len(m.Terrain))
	g.stamp[source], g.dist[source], g.parent[source] = g.gen, 0, -1
	g.open.push(tileNode{cost: 0, tile: source})
	open := func(t int32) bool { return standable(m, t) }
	for settled := 0; len(g.open) > 0; settled++ {
		if settled%cancelCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		n := g.open.pop()
		if n.cost != g.dist[n.tile] {
			continue
		}
		neighbours(m, n.tile, open, func(next int32) {
			c := n.cost + stepCost(m, n.tile, next)
			if g.stamp[next] == g.gen && g.dist[next] <= c {
				return
			}
			g.stamp[next], g.dist[next], g.parent[next] = g.gen, c, n.tile
			g.open.push(tileNode{cost: c, tile: next})
		})
	}
	return nil
}

// only reaches source alone: an agent on an automatic exit can only be
// carried onward by it.
func (g *gridSearch) only(m *world.Map, source int32) {
	g.prepare(len(m.Terrain))
	g.stamp[source], g.dist[source], g.parent[source] = g.gen, 0, -1
}

// path returns the tiles from the last run's source to tile, inclusive.
func (g *gridSearch) path(tile int32) []int32 {
	var reversed []int32
	for t := tile; t >= 0; t = g.parent[t] {
		reversed = append(reversed, t)
	}
	out := make([]int32, len(reversed))
	for i, t := range reversed {
		out[len(reversed)-1-i] = t
	}
	return out
}

// stepOnto is the cheapest cost of stepping from the searched component
// onto a tile that is not itself standable (an automatic exit), and the
// neighbour stepped from; ok is false when no reached neighbour touches it.
func (g *gridSearch) stepOnto(m *world.Map, tile int32) (cost int32, from int32, ok bool) {
	if !passable(m, tile) {
		return 0, 0, false
	}
	open := func(t int32) bool { return t == tile || standable(m, t) }
	best, bestFrom := int32(-1), int32(-1)
	neighbours(m, tile, open, func(n int32) {
		if n == tile {
			return
		}
		if c := g.cost(n); c >= 0 {
			total := c + stepCost(m, n, tile)
			if best < 0 || total < best {
				best, bestFrom = total, n
			}
		}
	})
	return best, bestFrom, best >= 0
}

type tileNode struct{ cost, tile int32 }

type tileHeap []tileNode

func (h *tileHeap) push(n tileNode) {
	*h = append(*h, n)
	a := *h
	for i := len(a) - 1; i > 0; {
		p := (i - 1) / 2
		if a[p].cost <= a[i].cost {
			break
		}
		a[i], a[p] = a[p], a[i]
		i = p
	}
}

func (h *tileHeap) pop() tileNode {
	a := *h
	top := a[0]
	last := len(a) - 1
	a[0] = a[last]
	a = a[:last]
	for i := 0; ; {
		l, r, m := 2*i+1, 2*i+2, i
		if l < len(a) && a[l].cost < a[m].cost {
			m = l
		}
		if r < len(a) && a[r].cost < a[m].cost {
			m = r
		}
		if m == i {
			break
		}
		a[i], a[m] = a[m], a[i]
		i = m
	}
	*h = a
	return top
}
