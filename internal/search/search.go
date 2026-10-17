// Package search finds exact tile paths over a compiled world.
//
// The search is A* over global tile states. Every move to an adjacent tile,
// orthogonal or diagonal, costs one; so does crossing a seam, and so does
// using an exit. On the goal map the heuristic is the Chebyshev distance to
// the goal tile, which never overestimates; elsewhere it is zero, because an
// exit can lead anywhere for the price of one step. Per-query state lives in
// pooled, generation-stamped arrays, so a search allocates only its result.
package search

import (
	"errors"
	"runtime"
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

// ErrInvalid reports a malformed query.
var ErrInvalid = errors.New("invalid query")

// Engine runs searches over one world. It is safe for concurrent use.
//
// Scratch space is sized to the whole world (about 17 bytes per tile), so it
// is kept on a bounded free list rather than a sync.Pool, which the garbage
// collector may empty between searches.
type Engine struct {
	world *world.World
	free  chan *scratch
}

// NewEngine prepares an engine for w.
func NewEngine(w *world.World) *Engine {
	return &Engine{world: w, free: make(chan *scratch, runtime.GOMAXPROCS(0))}
}

func (e *Engine) acquire() *scratch {
	select {
	case s := <-e.free:
		return s
	default:
		return newScratch(int(e.world.States))
	}
}

func (e *Engine) release(s *scratch) {
	select {
	case e.free <- s:
	default: // more concurrent searches than retained buffers: drop it
	}
}

// World returns the engine's world.
func (e *Engine) World() *world.World { return e.world }

type scratch struct {
	stamp  []uint32
	gen    uint32
	g      []int32
	parent []int32
	kind   []uint8 // Kind of arrival, plus autoBit for automatic exits
	via    []int32 // exit tile state an exit arrival was taken from, or -1
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

// FindPath runs one query.
func (e *Engine) FindPath(q Query) (Result, error) {
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
	s := e.acquire()
	defer e.release(s)
	s.reset()

	w := e.world
	start := w.State(q.Start.Map, q.Start.X, q.Start.Y)
	var goalState int32 = -1
	if q.GoalTile != nil {
		goalState = w.State(q.GoalTile.Map, q.GoalTile.X, q.GoalTile.Y)
		if goalState != start && !e.passable(q, q.GoalTile.Map, q.GoalTile.X, q.GoalTile.Y) {
			return Result{Status: NoPath, Duration: time.Since(started)}, nil
		}
	}
	r := searchRun{e: e, q: q, s: s, goalState: goalState}
	s.visit(start, 0, -1, uint8(Start), -1)
	s.open.push(node{f: r.h(q.Start.Map, q.Start.X, q.Start.Y), g: 0, state: start})
	result := Result{Status: NoPath}
	for len(s.open) > 0 {
		n := s.open.pop()
		if n.g != s.g[n.state] {
			continue // superseded by a cheaper arrival
		}
		m, x, y := w.Locate(n.state)
		if n.state == goalState || (q.GoalMap != nil && m == q.GoalMap) {
			result.Status = Found
			result.Steps = r.path(n.state)
			result.Cost = n.g
			break
		}
		result.Expanded++
		if result.Expanded > limit {
			result.Status = LimitReached
			break
		}
		r.expand(n, m, x, y)
	}
	result.Generated = r.generated
	result.Duration = time.Since(started)
	return result, nil
}

func (s *scratch) visit(state, g, parent int32, kind uint8, via int32) {
	s.stamp[state] = s.gen
	s.g[state] = g
	s.parent[state] = parent
	s.kind[state] = kind
	s.via[state] = via
}

type searchRun struct {
	e         *Engine
	q         Query
	s         *scratch
	goalState int32
	generated int
}

func (r *searchRun) h(m *world.Map, x, y int32) int32 {
	if r.q.GoalTile == nil || m != r.q.GoalTile.Map {
		return 0
	}
	return max(abs(x-r.q.GoalTile.X), abs(y-r.q.GoalTile.Y))
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
	for _, exit := range m.Exits[m.Tile(x, y)] {
		if exit.Automatic {
			continue
		}
		r.useExit(n.state, n.state, n.g+1, exit, false)
	}
}

// takeAutomaticExit handles stepping onto a tile whose exit triggers on
// arrival: the agent cannot stop there, only arrive at the destination.
// It returns true when the tile holds such an exit.
func (r *searchRun) takeAutomaticExit(from, tileState, g int32, m *world.Map, x, y int32) bool {
	exits := m.Exits[m.Tile(x, y)]
	automatic := false
	for _, exit := range exits {
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
			// An automatic exit: record the step onto the exit tile.
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
