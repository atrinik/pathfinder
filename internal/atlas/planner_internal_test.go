package atlas

import (
	"context"
	"errors"
	"testing"

	"github.com/atrinik/pathfinder/internal/world"
)

func openMap(width, height int32) *world.Map {
	tiles := width * height
	m := &world.Map{
		Path: "/t/open", Width: width, Height: height,
		Terrain: make([]uint32, tiles), Blocked: make([]bool, tiles), Road: make([]bool, tiles),
		Locks: map[int32]string{}, Exits: map[int32][]world.Exit{},
		Seams: [8]int32{-1, -1, -1, -1, -1, -1, -1, -1},
	}
	for i := range m.Terrain {
		m.Terrain[i] = world.TerrainWalk
	}
	return m
}

func TestPlannerAdmitsAtMostItsConcurrency(t *testing.T) {
	m := openMap(4, 4)
	a := Build(&world.World{Maps: []*world.Map{m}, States: 16})
	p := NewPlanner(a, 1)
	r := Request{Start: Position{Map: m}, GoalTile: &Position{Map: m, X: 3, Y: 3}}
	held := <-p.slots
	if _, err := p.Plan(context.Background(), r); !errors.Is(err, ErrBusy) {
		t.Fatalf("with every slot held: %v", err)
	}
	p.slots <- held
	if plan, err := p.Plan(context.Background(), r); err != nil || !plan.Found {
		t.Fatalf("with a free slot: %+v %v", plan, err)
	}
	if len(p.slots) != 1 {
		t.Fatal("the slot was not returned")
	}
}

func TestGridSearchesStopWhenCanceled(t *testing.T) {
	m := openMap(64, 64)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var g gridSearch
	if err := g.run(ctx, m, 0, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled search: %v", err)
	}
	if err := g.run(context.Background(), m, 0, nil); err != nil || g.cost(m.Tile(63, 63)) != 63*PlainStep {
		t.Fatalf("live search: %v cost %d", err, g.cost(m.Tile(63, 63)))
	}
}

func TestExhaustedPlansDoNotSimplify(t *testing.T) {
	m := openMap(64, 1)
	path := make([]int32, 64)
	for i := range path {
		path[i] = int32(i)
	}
	s := &scratch{}
	p := planner{s: s, work: -1}
	var plan Plan
	if err := p.walk(&plan, m, path, 7); !errors.Is(err, errLimit) {
		t.Fatalf("walk with no budget: %v", err)
	}
	if len(plan.Waypoints) != 0 || cap(s.keep) != 0 {
		t.Fatal("the path was simplified anyway")
	}
}

func TestSimplificationIsWindowedAndCancellable(t *testing.T) {
	// A long straight corridor, which unwindowed RDP would reduce to its
	// two ends after scanning it whole.
	m := openMap(1, 4000)
	var path []int32
	for y := int32(0); y < 4000; y++ {
		path = append(path, m.Tile(0, y))
	}
	s := &scratch{}
	kept, err := simplify(context.Background(), m, path, 1000, s)
	if err != nil {
		t.Fatal(err)
	}
	// No two kept points are more than a window apart along the path.
	last := int32(0)
	for _, i := range kept {
		if i-last > rdpWindow {
			t.Fatalf("kept %d after %d: wider than a window", i, last)
		}
		last = i
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := simplify(ctx, m, path, 1000, s); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled simplification: %v", err)
	}
}

func TestSimplificationStopsWhenCanceledInItsFinalPass(t *testing.T) {
	// Road and plain alternate, so every stretch is one tile long and no
	// RDP window runs: only the final pass sees the whole path.
	m := openMap(1, 4000)
	var path []int32
	for y := int32(0); y < 4000; y++ {
		tile := m.Tile(0, y)
		m.Road[tile] = y%2 == 0
		path = append(path, tile)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := simplify(ctx, m, path, 1, &scratch{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled final pass: %v", err)
	}
}

func TestPathReconstructionStopsWhenCanceled(t *testing.T) {
	m := openMap(5000, 1)
	var g gridSearch
	if err := g.run(context.Background(), m, 0, nil); err != nil {
		t.Fatal(err)
	}
	var buf []int32
	if path, err := g.path(context.Background(), 4999, &buf); err != nil || len(path) != 5000 {
		t.Fatalf("live: %d tiles, %v", len(path), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.path(ctx, 4999, &buf); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v", err)
	}
}
