// Package search finds exact tile paths over a compiled world.
//
// The search is A* over global tile states. Every move to an adjacent tile,
// orthogonal or diagonal, costs one; so does crossing a seam, and so does
// using an exit. The heuristic is admissible and consistent: on the goal map
// it is the Chebyshev distance to the goal, capped by the cheapest way of
// leaving and re-entering the map (one transition plus the distance from the
// nearest entry point, an exit arrival or a seam border, to the goal); off
// the goal map it is that re-entry bound. Per-query state lives in
// generation-stamped scratch arrays, of which the engine keeps one per
// allowed concurrent search, so a search allocates only its result.
package search

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/atrinik/pathfinder/internal/world"
)

// CornerPolicy decides when a diagonal step is allowed.
type CornerPolicy int

// Corner policies.
const (
	// CornersStrict needs both orthogonal neighbours of a diagonal passable.
	CornersStrict CornerPolicy = iota
	// CornersLoose needs only the diagonal's destination passable.
	CornersLoose
	// NoDiagonals allows only orthogonal steps.
	NoDiagonals
)

// Agent describes what the moving creature can traverse.
type Agent struct {
	TerrainMask uint32 // zero means world.TerrainWalk
	Keys        map[string]bool
	Corners     CornerPolicy
	StayOnMap   bool
	Avoided     map[int32]bool // map indexes the path must not enter
}

// Kind is how a step is reached.
type Kind int

// Step kinds.
const (
	Start Kind = iota
	Walk
	Seam
	ExitUsed
)

// Step is one position on a path.
type Step struct {
	Map       *world.Map
	X, Y      int32
	Kind      Kind
	Automatic bool
}

// Status is a search outcome.
type Status int

// Search outcomes.
const (
	Found Status = iota
	NoPath
	LimitReached
)

// Query is one path request. Exactly one of GoalTile or GoalMap is set.
type Query struct {
	Start       Position
	GoalTile    *Position
	GoalMap     *world.Map
	Agent       Agent
	Blocked     map[int32]bool // global states blocked for this query
	MaxExpanded int
}

// Position is a tile on a map.
type Position struct {
	Map  *world.Map
	X, Y int32
}

// Result is a search answer.
type Result struct {
	Status    Status
	Steps     []Step
	Cost      int32
	Expanded  int
	Generated int
	Duration  time.Duration
}

// DefaultMaxExpanded bounds a query that sets no limit.
const DefaultMaxExpanded = 4_000_000

// cancelCheckInterval is how many expansions run between context checks.
const cancelCheckInterval = 1024

var (
	// ErrInvalid reports a malformed query.
	ErrInvalid = errors.New("invalid query")
	// ErrBusy reports that every concurrent search slot is in use.
	ErrBusy = errors.New("all search slots are busy")
)

// Engine runs searches over one world. It is safe for concurrent use and
// runs at most a fixed number of searches at once; each holds scratch space
// sized to the whole world (about 17 bytes per tile).
type Engine struct {
	world *world.World
	slots chan struct{}
	free  chan *scratch
	// arrivals lists, per map, the tiles exits from anywhere arrive on.
	arrivals [][]int32
	// entrySides is, per map, a bitmask of the Directions a seam from a
	// neighbouring map enters it through (bit d: the side facing d).
	entrySides []uint8
}

// NewEngine prepares an engine for w that runs at most concurrency
// searches at once (at least one).
func NewEngine(w *world.World, concurrency int) *Engine {
	concurrency = max(concurrency, 1)
	e := &Engine{
		world:      w,
		slots:      make(chan struct{}, concurrency),
		free:       make(chan *scratch, concurrency),
		arrivals:   make([][]int32, len(w.Maps)),
		entrySides: make([]uint8, len(w.Maps)),
	}
	opposite := [8]world.Direction{
		world.North: world.South, world.East: world.West, world.South: world.North,
		world.West: world.East, world.NorthEast: world.SouthWest, world.SouthEast: world.NorthWest,
		world.SouthWest: world.NorthEast, world.NorthWest: world.SouthEast,
	}
	for _, m := range w.Maps {
		for _, exits := range m.Exits {
			for _, exit := range exits {
				dest := w.Maps[exit.To]
				if dest.InBounds(exit.X, exit.Y) {
					e.arrivals[exit.To] = append(e.arrivals[exit.To], dest.Tile(exit.X, exit.Y))
				}
			}
		}
		for dir, next := range m.Seams {
			if next >= 0 {
				e.entrySides[next] |= 1 << opposite[dir]
			}
		}
	}
	return e
}

// World returns the engine's world.
func (e *Engine) World() *world.World { return e.world }

func (e *Engine) acquire() (*scratch, error) {
	select {
	case e.slots <- struct{}{}:
	default:
		return nil, ErrBusy
	}
	select {
	case s := <-e.free:
		return s, nil
	default:
		// At most one scratch exists per slot, so this never exceeds the
		// configured concurrency.
		return newScratch(int(e.world.States)), nil
	}
}

func (e *Engine) release(s *scratch) {
	e.free <- s
	<-e.slots
}

type scratch struct {
	stamp  []uint32
	gen    uint32
	g      []int32
	parent []int32
	kind   []uint8 // Kind of arrival, plus autoBit for automatic exits
	via    []int32 // exit tile state an automatic exit was stepped onto, or -1
	open   heap
}

const autoBit = 0x80

func newScratch(states int) *scratch {
	return &scratch{
		stamp:  make([]uint32, states),
		g:      make([]int32, states),
		parent: make([]int32, states),
		kind:   make([]uint8, states),
		via:    make([]int32, states),
	}
}

func (s *scratch) reset() {
	s.gen++
	if s.gen == 0 {
		clear(s.stamp)
		s.gen = 1
	}
	s.open = s.open[:0]
}

func (s *scratch) seen(state int32) bool { return s.stamp[state] == s.gen }

func (s *scratch) visit(state, g, parent int32, kind uint8, via int32) {
	s.stamp[state] = s.gen
	s.g[state] = g
	s.parent[state] = parent
	s.kind[state] = kind
	s.via[state] = via
}

// hasAutomaticExit reports whether stepping onto a tile triggers an exit.
func hasAutomaticExit(m *world.Map, tile int32) bool {
	for _, exit := range m.Exits[tile] {
		if exit.Automatic {
			return true
		}
	}
	return false
}

// FindPath runs one query. It returns ErrBusy when every search slot is in
// use and the context's error when it is canceled mid-search.
func (e *Engine) FindPath(ctx context.Context, q Query) (Result, error) {
	started := time.Now()
	if q.Start.Map == nil || !q.Start.Map.InBounds(q.Start.X, q.Start.Y) {
		return Result{}, ErrInvalid
	}
	if (q.GoalTile == nil) == (q.GoalMap == nil) {
		return Result{}, ErrInvalid
	}
	if q.GoalTile != nil && (q.GoalTile.Map == nil || !q.GoalTile.Map.InBounds(q.GoalTile.X, q.GoalTile.Y)) {
		return Result{}, ErrInvalid
	}
	limit := q.MaxExpanded
	if limit <= 0 {
		limit = DefaultMaxExpanded
	}
	if q.Agent.TerrainMask == 0 {
		q.Agent.TerrainMask = world.TerrainWalk
	}
	w := e.world
	start := w.State(q.Start.Map, q.Start.X, q.Start.Y)
	var goalState int32 = -1
	if q.GoalTile != nil {
		goal := q.GoalTile
		goalState = w.State(goal.Map, goal.X, goal.Y)
		// Nobody can stop on an automatic exit, and an impassable tile is
		// unreachable (unless the agent already stands there).
		if hasAutomaticExit(goal.Map, goal.Map.Tile(goal.X, goal.Y)) ||
			(goalState != start && !e.passable(q, goal.Map, goal.X, goal.Y)) {
			return Result{Status: NoPath, Duration: time.Since(started)}, nil
		}
	}
	s, err := e.acquire()
	if err != nil {
		return Result{}, err
	}
	defer e.release(s)
	s.reset()

	r := searchRun{e: e, q: q, s: s, goalState: goalState, reentry: e.reentryBound(q)}
	s.visit(start, 0, -1, uint8(Start), -1)
	s.open.push(node{f: r.h(q.Start.Map, q.Start.X, q.Start.Y), g: 0, state: start})
	result := Result{Status: NoPath}
	for len(s.open) > 0 {
		n := s.open.pop()
		if n.g != s.g[n.state] {
			continue // superseded by a cheaper arrival
		}
		m, x, y := w.Locate(n.state)
		if r.isGoal(n.state, m, x, y) {
			result.Status = Found
			result.Steps = r.path(n.state)
			result.Cost = n.g
			break
		}
		if result.Expanded >= limit {
			result.Status = LimitReached
			break
		}
		if result.Expanded%cancelCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return Result{}, err
			}
		}
		result.Expanded++
		r.expand(n, m, x, y)
	}
	result.Generated = r.generated
	result.Duration = time.Since(started)
	return result, nil
}

// reentryBound is a lower bound on the cost of reaching the goal tile after
// leaving its map: one transition onto the goal map plus the Chebyshev
// distance from the nearest place a transition can arrive. It is MaxInt32
// when nothing enters the goal map.
func (e *Engine) reentryBound(q Query) int32 {
	if q.GoalTile == nil {
		return 0
	}
	goal := q.GoalTile
	m := goal.Map
	best := int32(math.MaxInt32)
	for _, tile := range e.arrivals[m.Index] {
		best = min(best, chebyshev(tile%m.Width, tile/m.Width, goal.X, goal.Y))
	}
	north, south := goal.Y, m.Height-1-goal.Y
	west, east := goal.X, m.Width-1-goal.X
	sideDistance := [8]int32{
		world.North: north, world.East: east, world.South: south, world.West: west,
		world.NorthEast: max(north, east), world.SouthEast: max(south, east),
		world.SouthWest: max(south, west), world.NorthWest: max(north, west),
	}
	sides := e.entrySides[m.Index]
	for dir := world.North; dir <= world.NorthWest; dir++ {
		if sides&(1<<dir) != 0 {
			best = min(best, sideDistance[dir])
		}
	}
	if best == math.MaxInt32 {
		return best
	}
	return best + 1
}

type searchRun struct {
	e         *Engine
	q         Query
	s         *scratch
	goalState int32
	reentry   int32
	generated int
}

func (r *searchRun) isGoal(state int32, m *world.Map, x, y int32) bool {
	if state != r.goalState && (r.q.GoalMap == nil || m != r.q.GoalMap) {
		return false
	}
	// An agent on an automatic exit is carried away; it cannot stop there.
	return !hasAutomaticExit(m, m.Tile(x, y))
}

func (r *searchRun) h(m *world.Map, x, y int32) int32 {
	if r.q.GoalTile == nil {
		return 0
	}
	if m != r.q.GoalTile.Map {
		if r.reentry == math.MaxInt32 {
			return 0 // the goal map cannot be entered; any bound would do
		}
		return r.reentry
	}
	return min(chebyshev(x, y, r.q.GoalTile.X, r.q.GoalTile.Y), r.reentry)
}

func chebyshev(x1, y1, x2, y2 int32) int32 {
	return max(abs(x1-x2), abs(y1-y2))
}

func abs(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

// passable reports whether the agent may stand on a tile.
func (e *Engine) passable(q Query, m *world.Map, x, y int32) bool {
	tile := m.Tile(x, y)
	if m.Terrain[tile]&q.Agent.TerrainMask == 0 || m.Blocked[tile] {
		return false
	}
	if key, locked := m.Locks[tile]; locked && !q.Agent.Keys[key] {
		return false
	}
	if q.Blocked[e.world.State(m, x, y)] {
		return false
	}
	return true
}

// enterable reports whether the agent may move onto a tile of map n coming
// from map m (avoided maps are closed except for the start and goal maps).
func (r *searchRun) enterable(from, n *world.Map, x, y int32) bool {
	if n != from {
		if r.q.Agent.StayOnMap || r.avoided(n) {
			return false
		}
	}
	return r.e.passable(r.q, n, x, y)
}

func (r *searchRun) avoided(n *world.Map) bool {
	if !r.q.Agent.Avoided[n.Index] || n == r.q.Start.Map {
		return false
	}
	if r.q.GoalTile != nil && n == r.q.GoalTile.Map {
		return false
	}
	return r.q.GoalMap != n
}

func (r *searchRun) relax(state, g, parent int32, kind uint8, via int32, m *world.Map, x, y int32) {
	s := r.s
	if s.seen(state) && s.g[state] <= g {
		return
	}
	s.visit(state, g, parent, kind, via)
	r.generated++
	s.open.push(node{f: g + r.h(m, x, y), g: g, state: state})
}

func (r *searchRun) expand(n node, m *world.Map, x, y int32) {
	tile := m.Tile(x, y)
	if hasAutomaticExit(m, tile) {
		// Standing on an automatic exit (the start, or an exit's arrival)
		// only leads onward through it.
		if !r.q.Agent.StayOnMap {
			for _, exit := range m.Exits[tile] {
				if exit.Automatic {
					r.useExit(n.state, n.state, n.g+1, exit, true)
				}
			}
		}
		return
	}
	w := r.e.world
	corners := r.q.Agent.Corners
	for dir := world.North; dir <= world.NorthWest; dir++ {
		d := world.Deltas[dir]
		diagonal := d[0] != 0 && d[1] != 0
		if diagonal && corners == NoDiagonals {
			continue
		}
		nm, nx, ny, ok := w.Step(m, x, y, d[0], d[1])
		if !ok || !r.enterable(m, nm, nx, ny) {
			continue
		}
		if diagonal && corners == CornersStrict && !r.cornersOpen(m, x, y, d) {
			continue
		}
		kind := uint8(Walk)
		if nm != m {
			kind = uint8(Seam)
		}
		next := w.State(nm, nx, ny)
		g := n.g + 1
		if r.takeAutomaticExit(n.state, next, g, nm, nx, ny) {
			continue
		}
		r.relax(next, g, n.state, kind, -1, nm, nx, ny)
	}
	// An applied (non-automatic) exit on the current tile leaves from here.
	if r.q.Agent.StayOnMap {
		return
	}
	for _, exit := range m.Exits[tile] {
		if !exit.Automatic {
			r.useExit(n.state, n.state, n.g+1, exit, false)
		}
	}
}

// takeAutomaticExit handles stepping onto a tile whose exit triggers on
// arrival: the agent cannot stop there, only arrive at the destination.
// It returns true when the tile holds such an exit.
func (r *searchRun) takeAutomaticExit(from, tileState, g int32, m *world.Map, x, y int32) bool {
	automatic := false
	for _, exit := range m.Exits[m.Tile(x, y)] {
		if !exit.Automatic {
			continue
		}
		automatic = true
		if !r.q.Agent.StayOnMap {
			r.useExit(from, tileState, g+1, exit, true)
		}
	}
	return automatic
}

func (r *searchRun) useExit(from, exitTile, g int32, exit world.Exit, automatic bool) {
	w := r.e.world
	dest := w.Maps[exit.To]
	if !dest.InBounds(exit.X, exit.Y) || r.avoided(dest) {
		return
	}
	if !r.e.passable(r.q, dest, exit.X, exit.Y) {
		return
	}
	kind := uint8(ExitUsed)
	if automatic {
		kind |= autoBit
	}
	r.relax(w.State(dest, exit.X, exit.Y), g, from, kind, exitTile, dest, exit.X, exit.Y)
}

func (r *searchRun) cornersOpen(m *world.Map, x, y int32, d [2]int32) bool {
	w := r.e.world
	for _, step := range [2][2]int32{{d[0], 0}, {0, d[1]}} {
		om, ox, oy, ok := w.Step(m, x, y, step[0], step[1])
		if !ok || !r.e.passable(r.q, om, ox, oy) {
			return false
		}
	}
	return true
}

func (r *searchRun) path(goal int32) []Step {
	s, w := r.s, r.e.world
	var reversed []Step
	for state := goal; state >= 0; state = s.parent[state] {
		m, x, y := w.Locate(state)
		raw := s.kind[state]
		kind := Kind(raw &^ autoBit)
		reversed = append(reversed, Step{Map: m, X: x, Y: y, Kind: kind, Automatic: raw&autoBit != 0})
		if via := s.via[state]; via >= 0 && via != s.parent[state] {
			// An automatic exit stepped onto: record the step onto its tile.
			vm, vx, vy := w.Locate(via)
			viaKind := Walk
			if pm, _, _ := w.Locate(s.parent[state]); pm != vm {
				viaKind = Seam
			}
			reversed = append(reversed, Step{Map: vm, X: vx, Y: vy, Kind: viaKind})
		}
	}
	steps := make([]Step, len(reversed))
	for i, step := range reversed {
		steps[len(reversed)-1-i] = step
	}
	return steps
}
