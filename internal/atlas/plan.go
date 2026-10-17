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

// ErrBusy reports that every plan slot of a Planner is in use.
var ErrBusy = errors.New("every plan slot is busy")

// Planner runs plans over one atlas with bounded concurrency, reusing each
// slot's scratch space. It is safe for concurrent use.
type Planner struct {
	atlas *Atlas
	slots chan *scratch
}

// scratch is the tile search space one plan uses; each grows to the largest
// map it has searched.
type scratch struct {
	start, goal, leg gridSearch
}

// NewPlanner returns a planner running at most concurrency plans at once.
func NewPlanner(a *Atlas, concurrency int) *Planner {
	p := &Planner{atlas: a, slots: make(chan *scratch, max(concurrency, 1))}
	for range cap(p.slots) {
		p.slots <- &scratch{}
	}
	return p
}

// Atlas returns the planner's atlas.
func (p *Planner) Atlas() *Atlas { return p.atlas }

// Plan plans like Atlas.Plan, or fails with ErrBusy when every slot is in
// use.
func (p *Planner) Plan(ctx context.Context, r Request) (Plan, error) {
	var s *scratch
	select {
	case s = <-p.slots:
	default:
		return Plan{}, ErrBusy
	}
	defer func() { p.slots <- s }()
	return p.atlas.plan(ctx, r, s)
}

// Plan finds the cheapest road-weighted route and returns its waypoints. It
// allocates its own scratch space; servers bound their work with a Planner.
func (a *Atlas) Plan(ctx context.Context, r Request) (Plan, error) {
	return a.plan(ctx, r, &scratch{})
}

func (a *Atlas) plan(ctx context.Context, r Request, s *scratch) (Plan, error) {
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
	p := planner{a: a, r: r, s: s}
	return p.run(ctx, started)
}

type planner struct {
	a *Atlas
	r Request
	s *scratch
	// fromExit is set when the start is an automatic exit, which carries the
	// walker onward at once.
	fromExit bool
}

const noConnector = int32(-1)

func (p *planner) run(ctx context.Context, started time.Time) (Plan, error) {
	a, r := p.a, p.r
	sm := r.Start.Map
	startTile := sm.Tile(r.Start.X, r.Start.Y)
	start, goalSearch := &p.s.start, &p.s.goal
	p.fromExit = isAutomaticExitTile(sm, startTile)
	if p.fromExit {
		start.only(sm, startTile)
	} else if err := start.run(ctx, sm, startTile); err != nil {
		return Plan{}, err
	}

	// Goal side: walking cost from each goal-map connector to the goal.
	var goalCost func(id int32) (int32, bool)
	best := int32(math.MaxInt32)
	if r.GoalTile != nil {
		gm := r.GoalTile.Map
		goalTile := gm.Tile(r.GoalTile.X, r.GoalTile.Y)
		// Nobody can stop on an automatic exit, even one they start on.
		if isAutomaticExitTile(gm, goalTile) || (!standable(gm, goalTile) && (gm != sm || goalTile != startTile)) {
			return Plan{Duration: time.Since(started)}, nil
		}
		if err := goalSearch.run(ctx, gm, goalTile); err != nil {
			return Plan{}, err
		}
		goalCost = func(id int32) (int32, bool) {
			c := a.Connectors[id]
			if c.Map != gm.Index || c.Component < 0 {
				return 0, false
			}
			cost := goalSearch.cost(gm.Tile(c.X, c.Y))
			return cost, cost >= 0
		}
		if gm == sm {
			if direct := start.cost(goalTile); direct >= 0 {
				best = direct
			}
		}
	} else {
		if sm == r.GoalMap && !p.fromExit {
			return p.assemble(ctx, nil, 0, started, 0)
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
	if p.fromExit {
		// Only the exit itself leads on.
		if id, ok := a.Lookup(sm, r.Start.X, r.Start.Y); ok {
			push(id, 0, noConnector)
		}
	}
	for _, id := range p.startConnectors() {
		if p.fromExit {
			break
		}
		c := a.Connectors[id]
		tile := sm.Tile(c.X, c.Y)
		cost := start.cost(tile)
		if c.Component < 0 {
			var ok bool
			if cost, _, ok = start.stepOnto(sm, tile); !ok {
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
		// (An automatic exit's own connector, such as the start's when the
		// walker starts on one, is never a goal: it has no component.)
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
	return p.assemble(ctx, chain, best, started, expanded)
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
func (p *planner) assemble(ctx context.Context, chain []int32, cost int32, started time.Time, expanded int) (Plan, error) {
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
			path := p.s.start.path(r.GoalTile.Map.Tile(r.GoalTile.X, r.GoalTile.Y))
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
	if !p.fromExit {
		p.walkTo(&plan, &p.s.start, sm, first, r.Spacing)
	}
	leg := &p.s.leg
	// transition marks the last waypoint as where a transition is used; the
	// start keeps its own waypoint, so one used from there is added after it.
	transition := func(kind WaypointKind, automatic bool, label string) {
		last := plan.Waypoints[len(plan.Waypoints)-1]
		if last.Kind == Start {
			plan.Waypoints = append(plan.Waypoints, last)
		}
		at := &plan.Waypoints[len(plan.Waypoints)-1]
		at.Kind, at.Automatic, at.Label = kind, automatic, label
	}
	for i := 0; i+1 < len(chain); i++ {
		u, v := chain[i], chain[i+1]
		edge := a.edge(u, v)
		cu, cv := a.Connectors[u], a.Connectors[v]
		mu, mv := a.World.Maps[cu.Map], a.World.Maps[cv.Map]
		switch edge.Kind {
		case WalkEdge:
			if err := leg.run(ctx, mu, mu.Tile(cu.X, cu.Y)); err != nil {
				return Plan{}, err
			}
			p.walkTo(&plan, leg, mu, cv, r.Spacing)
		case ExitEdge:
			transition(UseExit, edge.Automatic, edge.Label)
			add(mv, mv.Tile(cv.X, cv.Y), Arrive)
			plan.Tiles++
		case SeamEdge:
			transition(CrossSeam, false, "")
			add(mv, mv.Tile(cv.X, cv.Y), Arrive)
			plan.Tiles++
		}
	}
	last := a.Connectors[chain[len(chain)-1]]
	lm := a.World.Maps[last.Map]
	if r.GoalTile != nil {
		if err := leg.run(ctx, lm, lm.Tile(last.X, last.Y)); err != nil {
			return Plan{}, err
		}
		path := leg.path(r.GoalTile.Map.Tile(r.GoalTile.X, r.GoalTile.Y))
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
// waypoints. Where the path enters or leaves a road both tiles are kept, so
// a road leg is never replaced by a shortcut across open ground. Within each
// leg, Ramer-Douglas-Peucker keeps the points the path bends around by more
// than the tolerance (half a tile on roads, a tile elsewhere), so walking
// straight between waypoints stays on the chosen route; long straight runs
// are then split so that consecutive waypoints are at most spacing tiles
// apart.
func simplify(m *world.Map, path []int32, spacing int32) []int32 {
	keep := make([]bool, len(path))
	keep[0], keep[len(path)-1] = true, true
	lo := 0
	for i := 1; i <= len(path); i++ {
		if i < len(path) && m.Road[path[i]] == m.Road[path[lo]] {
			continue
		}
		// path[lo:i] is one road or off-road leg.
		keep[lo], keep[i-1] = true, true
		tolerance := rdpTolerance
		if m.Road[path[lo]] {
			tolerance = roadTolerance
		}
		rdp(m, path, lo, i-1, keep, tolerance)
		lo = i
	}
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

// rdpTolerance and roadTolerance are the largest distances, in tiles, a
// dropped path point may lie from the straight line between the waypoints
// around it, off and on roads.
const (
	rdpTolerance  = 1.0
	roadTolerance = 0.5
)

func rdp(m *world.Map, path []int32, lo, hi int, keep []bool, tolerance float64) {
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
	if worst > tolerance {
		keep[at] = true
		rdp(m, path, lo, at, keep, tolerance)
		rdp(m, path, at, hi, keep, tolerance)
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
