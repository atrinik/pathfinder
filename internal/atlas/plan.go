package atlas

import (
	"context"
	"errors"
	"math"
	"slices"
	"time"

	"github.com/atrinik/pathfinder/internal/world"
)

// DefaultSpacing is the default greatest distance between waypoints on one
// map: within the client's click-to-move reach.
const DefaultSpacing = 7

// WaypointKind is what to do at a waypoint.
type WaypointKind uint8

// Waypoint kinds.
const (
	// Start is where the plan begins.
	Start WaypointKind = iota
	// Walk is an intermediate point to walk (click) to.
	Walk
	// UseExit is an exit tile: apply the exit here, or, when automatic,
	// just step onto it.
	UseExit
	// CrossSeam is a border tile to step across into the neighbouring map.
	CrossSeam
	// Arrive is where an exit or seam crossing lands.
	Arrive
	// Goal is the destination.
	Goal
)

// Waypoint is one point of a plan.
type Waypoint struct {
	Map       *world.Map
	X, Y      int32
	Kind      WaypointKind
	Automatic bool
	Label     string
}

// Position is a tile on a map.
type Position struct {
	Map  *world.Map
	X, Y int32
}

// Request asks for a plan. Exactly one of GoalTile or GoalMap is set.
type Request struct {
	Start    Position
	GoalTile *Position
	GoalMap  *world.Map
	Avoided  map[int32]bool // map indexes to stay off (start and goal maps exempt)
	Spacing  int32          // greatest waypoint distance on one map; 0 selects DefaultSpacing
}

// Plan is a planner answer.
type Plan struct {
	Found     bool
	Waypoints []Waypoint
	// Cost is the road-weighted cost, in thirds of a move.
	Cost int32
	// Tiles is the number of tile moves along the plan, transitions included.
	Tiles    int32
	Expanded int
	Duration time.Duration
}

// ErrInvalid reports a malformed request.
var ErrInvalid = errors.New("invalid plan request")

// Plan finds the cheapest road-weighted route and returns its waypoints.
func (a *Atlas) Plan(ctx context.Context, r Request) (Plan, error) {
	started := time.Now()
	if err := ctx.Err(); err != nil {
		return Plan{}, err
	}
	if r.Start.Map == nil || !r.Start.Map.InBounds(r.Start.X, r.Start.Y) ||
		(r.GoalTile == nil) == (r.GoalMap == nil) ||
		(r.GoalTile != nil && (r.GoalTile.Map == nil || !r.GoalTile.Map.InBounds(r.GoalTile.X, r.GoalTile.Y))) {
		return Plan{}, ErrInvalid
	}
	if r.Spacing <= 0 {
		r.Spacing = DefaultSpacing
	}
	p := planner{a: a, r: r}
	return p.run(ctx, started)
}

type planner struct {
	a     *Atlas
	r     Request
	start gridSearch
	goal  gridSearch
}

const noConnector = int32(-1)

func (p *planner) run(ctx context.Context, started time.Time) (Plan, error) {
	a, r := p.a, p.r
	sm := r.Start.Map
	startTile := sm.Tile(r.Start.X, r.Start.Y)
	p.start.run(sm, startTile)

	// Goal side: walking cost from each goal-map connector to the goal.
	var goalCost func(id int32) (int32, bool)
	best := int32(math.MaxInt32)
	if r.GoalTile != nil {
		gm := r.GoalTile.Map
		goalTile := gm.Tile(r.GoalTile.X, r.GoalTile.Y)
		if !standable(gm, goalTile) && (gm != sm || goalTile != startTile) {
			return Plan{Duration: time.Since(started)}, nil
		}
		p.goal.run(gm, goalTile)
		goalCost = func(id int32) (int32, bool) {
			c := a.Connectors[id]
			if c.Map != gm.Index || c.Component < 0 {
				return 0, false
			}
			cost := p.goal.cost(gm.Tile(c.X, c.Y))
			return cost, cost >= 0
		}
		if gm == sm {
			if direct := p.start.cost(goalTile); direct >= 0 {
				best = direct
			}
		}
	} else {
		if sm == r.GoalMap {
			return p.assemble(nil, 0, started, 0)
		}
		goalCost = func(id int32) (int32, bool) {
			c := a.Connectors[id]
			return 0, c.Map == r.GoalMap.Index && c.Component >= 0
		}
	}

	// Dijkstra over connectors from the start's reachable connectors.
	dist := make(map[int32]int32, 64)
	parent := make(map[int32]int32, 64)
	var open tileHeap
	push := func(id, cost, from int32) {
		if old, ok := dist[id]; ok && old <= cost {
			return
		}
		dist[id], parent[id] = cost, from
		open.push(tileNode{cost: cost, tile: id})
	}
	for _, id := range p.startConnectors() {
		c := a.Connectors[id]
		tile := sm.Tile(c.X, c.Y)
		cost := p.start.cost(tile)
		if c.Component < 0 {
			var ok bool
			if cost, _, ok = p.start.stepOnto(sm, tile); !ok {
				continue
			}
		}
		if cost >= 0 {
			push(id, cost, noConnector)
		}
	}
	finish := noConnector
	expanded := 0
	for len(open) > 0 {
		n := open.pop()
		if n.cost != dist[n.tile] {
			continue
		}
		if n.cost >= best {
			break
		}
		if expanded%1024 == 0 {
			if err := ctx.Err(); err != nil {
				return Plan{}, err
			}
		}
		expanded++
		if extra, ok := goalCost(n.tile); ok && n.cost+extra < best {
			best, finish = n.cost+extra, n.tile
		}
		for _, e := range a.Out[n.tile] {
			to := a.Connectors[e.To]
			if p.avoided(to.Map) {
				continue
			}
			push(e.To, n.cost+e.Cost, n.tile)
		}
	}
	if best == math.MaxInt32 {
		return Plan{Expanded: expanded, Duration: time.Since(started)}, nil
	}
	var chain []int32
	for id := finish; id != noConnector; id = parent[id] {
		chain = append(chain, id)
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return p.assemble(chain, best, started, expanded)
}

func (p *planner) avoided(m int32) bool {
	r := p.r
	if !r.Avoided[m] || m == r.Start.Map.Index {
		return false
	}
	if r.GoalTile != nil && m == r.GoalTile.Map.Index {
		return false
	}
	return r.GoalMap == nil || m != r.GoalMap.Index
}

// startConnectors lists the start map's walkable-to connectors in id order
// (so equal-cost plans are stable); the start search's costs then decide
// which of them are reachable.
func (p *planner) startConnectors() []int32 {
	seen := map[int32]bool{}
	var ids []int32
	for _, members := range p.a.componentConnectors[p.r.Start.Map.Index] {
		for _, id := range members {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	slices.Sort(ids)
	return ids
}

// assemble expands a connector chain into waypoints.
func (p *planner) assemble(chain []int32, cost int32, started time.Time, expanded int) (Plan, error) {
	a, r := p.a, p.r
	plan := Plan{Found: true, Cost: cost, Expanded: expanded}
	add := func(m *world.Map, tile int32, kind WaypointKind) {
		plan.Waypoints = append(plan.Waypoints, Waypoint{Map: m, X: tile % m.Width, Y: tile / m.Width, Kind: kind})
	}
	sm := r.Start.Map
	startTile := sm.Tile(r.Start.X, r.Start.Y)
	add(sm, startTile, Start)
	if len(chain) == 0 {
		if r.GoalTile != nil {
			// A walk on the start map, possibly to the start itself.
			path := p.start.path(r.GoalTile.Map.Tile(r.GoalTile.X, r.GoalTile.Y))
			p.walk(&plan, sm, path, r.Spacing)
			plan.Waypoints[len(plan.Waypoints)-1].Kind = Goal
		}
		if r.GoalMap != nil {
			plan.Waypoints[0].Kind = Goal
		}
		plan.Duration = time.Since(started)
		return plan, nil
	}
	// Start to the first connector.
	first := a.Connectors[chain[0]]
	p.walkTo(&plan, &p.start, sm, first, r.Spacing)
	var scratch gridSearch
	for i := 0; i+1 < len(chain); i++ {
		u, v := chain[i], chain[i+1]
		edge := a.edge(u, v)
		cu, cv := a.Connectors[u], a.Connectors[v]
		mu, mv := a.World.Maps[cu.Map], a.World.Maps[cv.Map]
		switch edge.Kind {
		case WalkEdge:
			scratch.run(mu, mu.Tile(cu.X, cu.Y))
			p.walkTo(&plan, &scratch, mu, cv, r.Spacing)
		case ExitEdge:
			last := &plan.Waypoints[len(plan.Waypoints)-1]
			last.Kind, last.Automatic, last.Label = UseExit, edge.Automatic, edge.Label
			add(mv, mv.Tile(cv.X, cv.Y), Arrive)
			plan.Tiles++
		case SeamEdge:
			plan.Waypoints[len(plan.Waypoints)-1].Kind = CrossSeam
			add(mv, mv.Tile(cv.X, cv.Y), Arrive)
			plan.Tiles++
		}
	}
	last := a.Connectors[chain[len(chain)-1]]
	lm := a.World.Maps[last.Map]
	if r.GoalTile != nil {
		scratch.run(lm, lm.Tile(last.X, last.Y))
		path := scratch.path(r.GoalTile.Map.Tile(r.GoalTile.X, r.GoalTile.Y))
		p.walk(&plan, lm, path, r.Spacing)
	}
	plan.Waypoints[len(plan.Waypoints)-1].Kind = Goal
	plan.Duration = time.Since(started)
	return plan, nil
}

// walkTo appends the walk from a search's source to connector c.
func (p *planner) walkTo(plan *Plan, g *gridSearch, m *world.Map, c Connector, spacing int32) {
	tile := m.Tile(c.X, c.Y)
	if c.Component < 0 {
		// Step onto an automatic exit from its cheapest neighbour.
		_, from, ok := g.stepOnto(m, tile)
		if !ok {
			return
		}
		path := append(g.path(from), tile)
		p.walk(plan, m, path, spacing)
		return
	}
	p.walk(plan, m, g.path(tile), spacing)
}

// walk appends a tile path's waypoints (its first tile is already the last
// waypoint), at most spacing apart.
func (p *planner) walk(plan *Plan, m *world.Map, path []int32, spacing int32) {
	if len(path) < 2 {
		return
	}
	plan.Tiles += int32(len(path) - 1)
	for _, i := range simplify(m, path, spacing) {
		plan.Waypoints = append(plan.Waypoints, Waypoint{
			Map: m, X: path[i] % m.Width, Y: path[i] / m.Width, Kind: Walk,
		})
	}
}

// edge returns the cheapest edge from u to v.
func (a *Atlas) edge(u, v int32) Edge {
	var best Edge
	found := false
	for _, e := range a.Out[u] {
		if e.To == v && (!found || e.Cost < best.Cost) {
			best, found = e, true
		}
	}
	return best
}

// simplify picks the indexes (after the first) of a tile path to keep as
// waypoints. Ramer-Douglas-Peucker keeps the points the path bends around by
// more than a tile, so walking straight between waypoints stays on the
// chosen route (and its roads); long straight runs are then split so that
// consecutive waypoints are at most spacing tiles apart.
func simplify(m *world.Map, path []int32, spacing int32) []int32 {
	keep := make([]bool, len(path))
	keep[0], keep[len(path)-1] = true, true
	rdp(m, path, 0, len(path)-1, keep)
	var out []int32
	last := 0
	for i := 1; i < len(path); i++ {
		if !keep[i] {
			continue
		}
		// Split a long leg at path points no more than spacing apart.
		for chebyshevTiles(m, path[last], path[i]) > spacing {
			j := last + 1
			for j+1 < i && chebyshevTiles(m, path[last], path[j+1]) <= spacing {
				j++
			}
			out = append(out, int32(j))
			last = j
		}
		out = append(out, int32(i))
		last = i
	}
	return out
}

// rdpTolerance is the largest distance, in tiles, a dropped path point may
// lie from the straight line between the waypoints around it.
const rdpTolerance = 1.0

func rdp(m *world.Map, path []int32, lo, hi int, keep []bool) {
	if hi-lo < 2 {
		return
	}
	ax, ay := float64(path[lo]%m.Width), float64(path[lo]/m.Width)
	bx, by := float64(path[hi]%m.Width), float64(path[hi]/m.Width)
	dx, dy := bx-ax, by-ay
	length := math.Hypot(dx, dy)
	worst, at := 0.0, -1
	for i := lo + 1; i < hi; i++ {
		px, py := float64(path[i]%m.Width), float64(path[i]/m.Width)
		var d float64
		if length == 0 {
			d = math.Hypot(px-ax, py-ay)
		} else {
			d = math.Abs(dx*(ay-py)-dy*(ax-px)) / length
		}
		if d > worst {
			worst, at = d, i
		}
	}
	if worst > rdpTolerance {
		keep[at] = true
		rdp(m, path, lo, at, keep)
		rdp(m, path, at, hi, keep)
	}
}

func chebyshevTiles(m *world.Map, a, b int32) int32 {
	dx, dy := a%m.Width-b%m.Width, a/m.Width-b/m.Width
	if dx < 0 {
		dx = -dx
	}
	if dy < 0 {
		dy = -dy
	}
	return max(dx, dy)
}
