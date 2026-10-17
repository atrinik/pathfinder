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
