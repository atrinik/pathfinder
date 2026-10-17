package atlas

import (
	"context"
	"errors"
	"fmt"
	"math"
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
	// MaxExpanded bounds one plan's work: each connector expanded or looked
	// at, edge examined, tile searched and waypoint emitted spends one. Zero
	// means no bound.
	MaxExpanded int
}

// Plan is a planner answer.
type Plan struct {
	Found bool
	// Work is the budget the plan spent (see Request.MaxExpanded).
	Work int
	// LimitReached is set when the plan spent its MaxExpanded work budget.
	LimitReached bool
	Waypoints    []Waypoint
	// Cost is the road-weighted cost, in thirds of a move.
	Cost int32
	// Tiles is the number of tile moves along the plan, transitions included.
	Tiles    int32
	Expanded int
	Duration time.Duration
}

// ErrInvalid reports a malformed request.
var ErrInvalid = errors.New("invalid plan request")

// ErrCorrupt reports an atlas whose walk costs disagree with the content: a
// re-derived walk cost something other than its edge.
var ErrCorrupt = errors.New("atlas walk cost disagrees with the content")

// ErrBusy reports that every plan slot of a Planner is in use.
var ErrBusy = errors.New("every plan slot is busy")

// Planner runs plans over one atlas with bounded concurrency, reusing each
// slot's scratch space. It is safe for concurrent use.
type Planner struct {
	atlas *Atlas
	slots chan *scratch
}

// scratch is the search space one plan uses: tile searches, which grow to
// the largest map searched, and the generation-stamped connector search.
type scratch struct {
	start, goal, leg gridSearch
	dist, parent     []int32
	via              []int32 // the index in a.Out[parent] of the edge taken
	origin           []int32 // the seam start a seeded connector came from, or -1
	stamp            []uint32
	gen              uint32
	open             tileHeap
	path             []int32 // reconstructed walk legs
	chain            []int32 // the connector chain of a plan
	seams            []seamStart
	keep             []bool  // simplification marks
	kept             []int32 // simplified waypoint indexes
}

// prepareConnectors readies the connector search for n connectors.
func (s *scratch) prepareConnectors(n int) {
	if len(s.dist) < n {
		s.dist = make([]int32, n)
		s.parent = make([]int32, n)
		s.via = make([]int32, n)
		s.origin = make([]int32, n)
		s.stamp = make([]uint32, n)
		s.gen = 0
	}
	s.gen++
	if s.gen == 0 {
		clear(s.stamp)
		s.gen = 1
	}
	s.open = s.open[:0]
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
	p := planner{a: a, r: r, s: s, ctx: ctx}
	return p.run(ctx, started)
}

type planner struct {
	a   *Atlas
	r   Request
	s   *scratch
	ctx context.Context
	// work is the plan's remaining budget: connectors expanded, tiles
	// searched and waypoints emitted each spend one.
	work, budget int
	// fromExit is set when the start is an automatic exit, which carries the
	// walker onward at once.
	fromExit bool
	// seamStarts are the tiles across a seam an agent standing on a tile
	// nobody else can stand on may step straight onto; finishSeam is the one
	// a plan ends through without reaching any connector, or -1.
	seamStarts []seamStart
	finishSeam int
}

type seamStart struct {
	m    *world.Map
	tile int32
}

const noConnector = int32(-1)

// errLimit stops a plan that has spent its work budget.
var errLimit = errors.New("plan work limit reached")

func (p *planner) run(ctx context.Context, started time.Time) (Plan, error) {
	plan, err := p.search(ctx, started)
	if err == nil && p.work < 0 {
		err = errLimit
	}
	if errors.Is(err, errLimit) {
		return Plan{
			LimitReached: true, Expanded: plan.Expanded, Work: p.budget,
			Duration: time.Since(started),
		}, nil
	}
	plan.Work = p.budget - p.work
	return plan, err
}

func (p *planner) search(ctx context.Context, started time.Time) (Plan, error) {
	a, r := p.a, p.r
	p.work = math.MaxInt
	if r.MaxExpanded > 0 {
		p.work = r.MaxExpanded
	}
	p.budget = p.work
	p.finishSeam = -1
	p.seamStarts = p.s.seams[:0]
	sm := r.Start.Map
	startTile := sm.Tile(r.Start.X, r.Start.Y)
	start, goalSearch, side := &p.s.start, &p.s.goal, &p.s.leg
	p.fromExit = isAutomaticExitTile(sm, startTile)
	// A goal the start already satisfies needs no search.
	if !p.fromExit && (r.GoalMap == sm ||
		(r.GoalTile != nil && r.GoalTile.Map == sm && sm.Tile(r.GoalTile.X, r.GoalTile.Y) == startTile)) {
		start.only(sm, startTile)
		return p.assemble(ctx, nil, 0, started, 0)
	}
	if plan, ok := p.exitOntoGoal(startTile, started); ok {
		return plan, nil
	}
	if p.fromExit {
		start.only(sm, startTile)
	} else if err := start.run(ctx, sm, startTile, &p.work); err != nil {
		return Plan{}, err
	}

	// Goal side: walking cost from each goal-map connector to the goal.
	var goalCost func(id int32) (int32, bool)
	best := int32(math.MaxInt32)
	var goalTile int32
	if r.GoalTile != nil {
		gm := r.GoalTile.Map
		goalTile = gm.Tile(r.GoalTile.X, r.GoalTile.Y)
		// Nobody can stop on an automatic exit, even one they start on.
		if isAutomaticExitTile(gm, goalTile) || (!standable(gm, goalTile) && (gm != sm || goalTile != startTile)) {
			return Plan{Duration: time.Since(started)}, nil
		}
		if err := goalSearch.run(ctx, gm, goalTile, &p.work); err != nil {
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
		goalCost = func(id int32) (int32, bool) {
			c := a.Connectors[id]
			return 0, c.Map == r.GoalMap.Index && c.Component >= 0
		}
	}

	// Dijkstra over connectors from the start's reachable connectors.
	s := p.s
	s.prepareConnectors(len(a.Connectors))
	// via is the index of the edge taken in a.Out[from], so assembly reads
	// each hop's edge directly instead of searching for it again.
	pushVia := func(id, cost, from, via, origin int32) {
		if s.stamp[id] == s.gen && s.dist[id] <= cost {
			return
		}
		s.stamp[id], s.dist[id], s.parent[id], s.via[id], s.origin[id] = s.gen, cost, from, via, origin
		s.open.push(tileNode{cost: cost, tile: id})
	}
	push := func(id, cost, from, origin int32) { pushVia(id, cost, from, -1, origin) }
	// The start's own tile may be a connector nothing else walks to: an
	// automatic exit, which is then the only way on, or an applied exit on a
	// tile nobody can enter.
	if id, ok := a.Lookup(sm, r.Start.X, r.Start.Y); ok {
		push(id, 0, noConnector, -1)
	}
	seedExit := func(tile int32) {
		id, ok := a.Lookup(sm, tile%sm.Width, tile/sm.Width)
		if !ok || a.Connectors[id].Component >= 0 {
			return
		}
		if cost, _, ok := start.stepOnto(sm, tile); ok {
			push(id, cost, noConnector, -1)
		}
	}
	if !p.fromExit {
		// Automatic exits beside the start, which perhaps only the start
		// (on a tile nobody else can stand on) touches.
		neighbours(sm, startTile, func(t int32) bool { return passable(sm, t) && isAutomaticExitTile(sm, t) }, seedExit)
		if err := p.seedReached(ctx, start, sm, startTile, func(id, cost int32) {
			push(id, cost, noConnector, -1)
		}); err != nil {
			return Plan{}, err
		}
		if !standable(sm, startTile) {
			err := p.seedSeamStarts(ctx, side, goalTile, &best, push)
			p.s.seams = p.seamStarts
			if err != nil {
				return Plan{}, err
			}
		}
	}
	finish := noConnector
	expanded := 0
	for len(s.open) > 0 {
		n := s.open.pop()
		if n.cost != s.dist[n.tile] {
			continue
		}
		if n.cost >= best {
			break
		}
		// A connector that is itself the goal ends the search here, before
		// it is charged or expanded: nothing can arrive cheaper.
		if extra, ok := goalCost(n.tile); ok && extra == 0 {
			best, finish, p.finishSeam = n.cost, n.tile, -1
			continue
		}
		if p.work--; p.work < 0 {
			return Plan{Expanded: expanded}, errLimit
		}
		if expanded%1024 == 0 {
			if err := ctx.Err(); err != nil {
				return Plan{}, err
			}
		}
		expanded++
		// (An automatic exit's own connector, such as the start's when the
		// walker starts on one, is never a goal: it has no component.)
		if extra, ok := goalCost(n.tile); ok && int64(n.cost)+int64(extra) < int64(best) {
			best, finish, p.finishSeam = n.cost+extra, n.tile, -1
		}
		for k, e := range a.Out[n.tile] {
			// Each edge examined spends work too: a connector may have as many
			// walks as its component has connectors.
			if p.work--; p.work < 0 {
				return Plan{Expanded: expanded}, errLimit
			}
			if k%cancelCheckInterval == cancelCheckInterval-1 {
				if err := ctx.Err(); err != nil {
					return Plan{}, err
				}
			}
			to := a.Connectors[e.To]
			if p.avoided(to.Map) {
				continue
			}
			// Costs from a damaged atlas file must not wrap around.
			if total := int64(n.cost) + int64(e.Cost); total < math.MaxInt32 {
				pushVia(e.To, int32(total), n.tile, int32(k), -1)
			}
		}
	}
	if best == math.MaxInt32 {
		return Plan{Expanded: expanded, Duration: time.Since(started)}, nil
	}
	chain := s.chain[:0]
	for id := finish; id != noConnector; id = s.parent[id] {
		if len(chain)%cancelCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return Plan{}, err
			}
		}
		chain = append(chain, id)
	}
	s.chain = chain
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		if i%cancelCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return Plan{}, err
			}
		}
		chain[i], chain[j] = chain[j], chain[i]
	}
	plan, err := p.assemble(ctx, chain, best, started, expanded)
	plan.Expanded = expanded
	return plan, err
}

// seedSeamStarts lets an agent on a tile nobody else can stand on step
// straight across a seam: it seeds the connectors walkable from each tile
// it can land on, and the goal when it lies there.
func (p *planner) seedSeamStarts(ctx context.Context, side *gridSearch, goalTile int32, best *int32,
	push func(id, cost, from, origin int32)) error {
	a, r := p.a, p.r
	sm := r.Start.Map
	for dir := world.North; dir <= world.NorthWest; dir++ {
		d := world.Deltas[dir]
		if sm.InBounds(r.Start.X+d[0], r.Start.Y+d[1]) {
			continue // an ordinary step, which the start search covers
		}
		// A seam may lead back onto this same map.
		n, nx, ny, ok := a.World.Step(sm, r.Start.X, r.Start.Y, d[0], d[1])
		if !ok || p.avoided(n.Index) || !a.cornersOpen(sm, r.Start.X, r.Start.Y, d) {
			continue
		}
		dest := n.Tile(nx, ny)
		origin := int32(len(p.seamStarts))
		if !standable(n, dest) {
			// Stepping across onto an automatic exit takes it.
			if passable(n, dest) && isAutomaticExitTile(n, dest) {
				if id, ok := a.Lookup(n, nx, ny); ok {
					p.seamStarts = append(p.seamStarts, seamStart{n, dest})
					push(id, Transition, noConnector, origin)
				}
			}
			continue
		}
		p.seamStarts = append(p.seamStarts, seamStart{n, dest})
		if r.GoalMap == n {
			// Entering the goal map costs the one transition, which nothing
			// beats; there is nothing to search there.
			if Transition < *best {
				*best, p.finishSeam = Transition, int(origin)
			}
			continue
		}
		if err := side.run(ctx, n, dest, &p.work); err != nil {
			return err
		}
		switch {
		case r.GoalTile != nil && r.GoalTile.Map == n:
			if c := side.cost(goalTile); c >= 0 && Transition+c < *best {
				*best, p.finishSeam = Transition+c, int(origin)
			}
		}
		if err := p.seedReached(ctx, side, n, dest, func(id, cost int32) {
			push(id, Transition+cost, noConnector, origin)
		}); err != nil {
			return err
		}
	}
	return nil
}

// exitOntoGoal answers a start whose own exit lands on the goal map or
// goal tile: any route to another map needs at least one transition, and a
// start on an automatic exit is carried by it wherever it leads, so taking
// it is already best and nothing needs searching. Otherwise the start's
// applied exits are usable from it.
func (p *planner) exitOntoGoal(startTile int32, started time.Time) (Plan, bool) {
	r := p.r
	sm := r.Start.Map
	for _, exit := range sm.Exits[startTile] {
		if exit.Automatic != p.fromExit {
			continue
		}
		dest := p.a.World.Maps[exit.To]
		// Within one map an optional exit can be beaten by walking; a forced
		// one is the only way on.
		if (dest == sm && !exit.Automatic) || !dest.InBounds(exit.X, exit.Y) ||
			!standable(dest, dest.Tile(exit.X, exit.Y)) {
			continue
		}
		switch {
		case r.GoalMap != nil && dest != r.GoalMap,
			r.GoalTile != nil && (dest != r.GoalTile.Map || exit.X != r.GoalTile.X || exit.Y != r.GoalTile.Y):
			continue
		}
		start := Waypoint{Map: sm, X: r.Start.X, Y: r.Start.Y, Kind: Start}
		use := start
		use.Kind, use.Automatic, use.Label = UseExit, exit.Automatic, exit.Label
		p.work -= 3
		return Plan{
			Found: true, Cost: Transition, Tiles: 1, Duration: time.Since(started),
			Waypoints: []Waypoint{start, use, {Map: dest, X: exit.X, Y: exit.Y, Kind: Goal}},
		}, true
	}
	return Plan{}, false
}

// seedReached offers the connectors walkable from the components a tile
// search from source reached (the source's own, or, from a tile nobody else
// can stand on, its neighbours'), with their costs. Each connector looked at
// spends a unit of work.
func (p *planner) seedReached(ctx context.Context, g *gridSearch, m *world.Map, source int32,
	seed func(id, cost int32)) error {
	labels := p.a.Components[m.Index]
	var comps [9]int32
	n := 0
	addComp := func(c int32) {
		if c < 0 {
			return
		}
		for _, have := range comps[:n] {
			if have == c {
				return
			}
		}
		comps[n] = c
		n++
	}
	addComp(labels[source])
	neighbours(m, source, func(t int32) bool { return standable(m, t) && g.cost(t) >= 0 }, func(t int32) {
		addComp(labels[t])
	})
	for _, comp := range comps[:n] {
		for k, id := range p.a.componentConnectors[m.Index][comp] {
			if k%cancelCheckInterval == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			if p.work--; p.work < 0 {
				return errLimit
			}
			c := p.a.Connectors[id]
			tile := m.Tile(c.X, c.Y)
			cost := g.cost(tile)
			if c.Component < 0 {
				var ok bool
				if cost, _, ok = g.stepOnto(m, tile); !ok {
					continue
				}
			}
			if cost >= 0 {
				seed(id, cost)
			}
		}
	}
	return nil
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

// assemble expands a connector chain into waypoints.
func (p *planner) assemble(ctx context.Context, chain []int32, cost int32, started time.Time, expanded int) (Plan, error) {
	a, r := p.a, p.r
	plan := Plan{Found: true, Cost: cost, Expanded: expanded}
	// Every waypoint spends a unit of work; run reports an overspent budget.
	add := func(m *world.Map, tile int32, kind WaypointKind) {
		p.work--
		plan.Waypoints = append(plan.Waypoints, Waypoint{Map: m, X: tile % m.Width, Y: tile / m.Width, Kind: kind})
	}
	// transition marks the last waypoint as where a transition is used; the
	// start keeps its own waypoint, so one used from there is added after it.
	transition := func(kind WaypointKind, automatic bool, label string) {
		last := plan.Waypoints[len(plan.Waypoints)-1]
		if last.Kind == Start {
			p.work--
			plan.Waypoints = append(plan.Waypoints, last)
		}
		at := &plan.Waypoints[len(plan.Waypoints)-1]
		at.Kind, at.Automatic, at.Label = kind, automatic, label
	}
	sm := r.Start.Map
	startTile := sm.Tile(r.Start.X, r.Start.Y)
	add(sm, startTile, Start)
	leg := &p.s.leg
	origin := p.finishSeam
	if len(chain) > 0 {
		origin = int(p.s.origin[chain[0]])
	}
	switch {
	case origin >= 0:
		// Straight across a seam from a tile nobody else can stand on.
		ss := p.seamStarts[origin]
		transition(CrossSeam, false, "")
		add(ss.m, ss.tile, Arrive)
		plan.Tiles++
		switch {
		case isAutomaticExitTile(ss.m, ss.tile):
			leg.only(ss.m, ss.tile) // it takes the walker on at once
		case len(chain) == 0 && r.GoalMap != nil:
			// Arriving is reaching the goal map.
		default:
			if err := leg.run(ctx, ss.m, ss.tile, &p.work); err != nil {
				return Plan{}, err
			}
		}
		if len(chain) == 0 {
			if r.GoalTile != nil {
				if err := p.walk(&plan, ss.m, leg.path(r.GoalTile.Map.Tile(r.GoalTile.X, r.GoalTile.Y), &p.s.path), r.Spacing); err != nil {
					return Plan{}, err
				}
			}
			plan.Waypoints[len(plan.Waypoints)-1].Kind = Goal
			plan.Duration = time.Since(started)
			return plan, nil
		}
		if first := a.Connectors[chain[0]]; ss.m.Tile(first.X, first.Y) != ss.tile {
			if err := p.walkTo(&plan, leg, ss.m, first, r.Spacing); err != nil {
				return Plan{}, err
			}
		}
	case len(chain) == 0:
		if r.GoalTile != nil {
			// A walk on the start map, possibly to the start itself.
			path := p.s.start.path(r.GoalTile.Map.Tile(r.GoalTile.X, r.GoalTile.Y), &p.s.path)
			if err := p.walk(&plan, sm, path, r.Spacing); err != nil {
				return Plan{}, err
			}
			plan.Waypoints[len(plan.Waypoints)-1].Kind = Goal
		}
		if r.GoalMap != nil {
			plan.Waypoints[0].Kind = Goal
		}
		plan.Duration = time.Since(started)
		return plan, nil
	case !p.fromExit:
		// Start to the first connector.
		if err := p.walkTo(&plan, &p.s.start, sm, a.Connectors[chain[0]], r.Spacing); err != nil {
			return Plan{}, err
		}
	}
	for i := 0; i+1 < len(chain); i++ {
		if i%256 == 0 {
			if err := ctx.Err(); err != nil {
				return Plan{}, err
			}
			if p.work < 0 {
				return Plan{}, errLimit
			}
		}
		u, v := chain[i], chain[i+1]
		edge := a.Out[u][p.s.via[v]]
		cu, cv := a.Connectors[u], a.Connectors[v]
		mu, mv := a.World.Maps[cu.Map], a.World.Maps[cv.Map]
		switch edge.Kind {
		case WalkEdge:
			if err := leg.run(ctx, mu, mu.Tile(cu.X, cu.Y), &p.work); err != nil {
				return Plan{}, err
			}
			// The walk is re-derived from the content, so its real cost
			// must be the edge's; anything else is a damaged atlas.
			tile := mv.Tile(cv.X, cv.Y)
			real := leg.cost(tile)
			if cv.Component < 0 {
				real, _, _ = leg.stepOnto(mv, tile)
			}
			if real != edge.Cost {
				return Plan{}, fmt.Errorf("%w: %s %d,%d to %d,%d costs %d, the atlas says %d",
					ErrCorrupt, mu.Path, cu.X, cu.Y, cv.X, cv.Y, real, edge.Cost)
			}
			if err := p.walkTo(&plan, leg, mu, cv, r.Spacing); err != nil {
				return Plan{}, err
			}
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
	if goal := r.GoalTile; goal != nil && (goal.Map != lm || goal.X != last.X || goal.Y != last.Y) {
		if err := leg.run(ctx, lm, lm.Tile(last.X, last.Y), &p.work); err != nil {
			return Plan{}, err
		}
		path := leg.path(r.GoalTile.Map.Tile(r.GoalTile.X, r.GoalTile.Y), &p.s.path)
		if err := p.walk(&plan, lm, path, r.Spacing); err != nil {
			return Plan{}, err
		}
	}
	plan.Waypoints[len(plan.Waypoints)-1].Kind = Goal
	plan.Duration = time.Since(started)
	return plan, nil
}

// walkTo appends the walk from a search's source to connector c.
func (p *planner) walkTo(plan *Plan, g *gridSearch, m *world.Map, c Connector, spacing int32) error {
	tile := m.Tile(c.X, c.Y)
	if c.Component < 0 {
		// Step onto an automatic exit from its cheapest neighbour.
		_, from, ok := g.stepOnto(m, tile)
		if !ok {
			return nil
		}
		p.s.path = append(g.path(from, &p.s.path), tile)
		return p.walk(plan, m, p.s.path, spacing)
	}
	return p.walk(plan, m, g.path(tile, &p.s.path), spacing)
}

// walk appends a tile path's waypoints (its first tile is already the last
// waypoint), at most spacing apart. Each waypoint spends one unit of work.
func (p *planner) walk(plan *Plan, m *world.Map, path []int32, spacing int32) error {
	if len(path) < 2 {
		return nil
	}
	// Simplifying costs time in proportion to the path, whose tiles the leg
	// search has already paid for; an exhausted budget stops before it.
	if p.work < 0 {
		return errLimit
	}
	plan.Tiles += int32(len(path) - 1)
	kept, err := simplify(p.ctx, m, path, spacing, p.s)
	if err != nil {
		return err
	}
	for _, i := range kept {
		if p.work--; p.work < 0 {
			return errLimit
		}
		plan.Waypoints = append(plan.Waypoints, Waypoint{
			Map: m, X: path[i] % m.Width, Y: path[i] / m.Width, Kind: Walk,
		})
	}
	return nil
}

// simplify picks the indexes (after the first) of a tile path to keep as
// waypoints. Where the path enters or leaves a road both tiles are kept, so
// a road leg is never replaced by a shortcut across open ground. Within each
// leg, Ramer-Douglas-Peucker keeps the points the path bends around by more
// than the tolerance (half a tile on roads, a tile elsewhere), so walking
// straight between waypoints stays on the chosen route; long straight runs
// are then split so that consecutive waypoints are at most spacing tiles
// apart. Finally every segment must be walkable in a straight line, so a
// waypoint is never dropped from a bend that rounds an obstacle.
//
// RDP runs over windows of at most rdpWindow path points, each end kept, so
// its work grows linearly with the path however it winds; the context is
// checked between windows.
func simplify(ctx context.Context, m *world.Map, path []int32, spacing int32, s *scratch) ([]int32, error) {
	keep := s.keep[:0]
	for range path {
		keep = append(keep, false)
	}
	s.keep = keep
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
		for start := lo; start < i-1; start += rdpWindow {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			end := min(start+rdpWindow, i-1)
			keep[end] = true
			rdp(m, path, start, end, keep, tolerance)
		}
		lo = i
	}
	out := s.kept[:0]
	last := 0
	for i := 1; i < len(path); i++ {
		if i%cancelCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if !keep[i] {
			continue
		}
		// Split a long leg at path points no more than spacing apart.
		for chebyshevTiles(m, path[last], path[i]) > spacing {
			j := last + 1
			for j+1 < i && chebyshevTiles(m, path[last], path[j+1]) <= spacing {
				j++
			}
			out = appendWalkable(m, path, out, last, j)
			last = j
		}
		out = appendWalkable(m, path, out, last, i)
		last = i
	}
	s.kept = out
	return out, nil
}

// appendWalkable appends the index hi, preceded by whatever points of
// path[lo:hi] walking straight from path[lo] to path[hi] needs. A segment of
// an all-road stretch must also stay on road tiles.
func appendWalkable(m *world.Map, path []int32, out []int32, lo, hi int) []int32 {
	road := true
	for _, t := range path[lo : hi+1] {
		road = road && m.Road[t]
	}
	if hi-lo > 1 && !straightWalkable(m, path[lo], path[hi], road) {
		mid := (lo + hi) / 2
		out = appendWalkable(m, path, out, lo, mid)
		lo = mid
		return appendWalkable(m, path, out, lo, hi)
	}
	return append(out, int32(hi))
}

// straightWalkable reports whether walking from a to b one eight-way step at
// a time along the line between them stays on standable tiles (b itself may
// be an automatic exit) under strict corners, and, when road is set, on road
// tiles.
func straightWalkable(m *world.Map, a, b int32, road bool) bool {
	ax, ay := a%m.Width, a/m.Width
	dx, dy := b%m.Width-ax, b/m.Width-ay
	n := max(abs32(dx), abs32(dy))
	px, py := ax, ay
	for k := int32(1); k <= n; k++ {
		x := ax + int32(math.Round(float64(k*dx)/float64(n)))
		y := ay + int32(math.Round(float64(k*dy)/float64(n)))
		tile := m.Tile(x, y)
		if k < n && !standable(m, tile) || k == n && !passable(m, tile) || road && !m.Road[tile] {
			return false
		}
		if x != px && y != py && (!passable(m, m.Tile(x, py)) || !passable(m, m.Tile(px, y))) {
			return false
		}
		px, py = x, y
	}
	return true
}

// rdpTolerance and roadTolerance are the largest distances, in tiles, a
// dropped path point may lie from the straight line between the waypoints
// around it, off and on roads.
const (
	rdpTolerance  = 1.0
	roadTolerance = 0.5
)

// rdpWindow bounds the path points one RDP pass considers.
const rdpWindow = 32

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
