package search_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/atrinik/pathfinder/internal/content"
	"github.com/atrinik/pathfinder/internal/search"
	"github.com/atrinik/pathfinder/internal/world"
)

// realWorld loads the content checkout named by PATHFINDER_CONTENT once.
var realWorld = sync.OnceValues(func() (*world.World, error) {
	root := os.Getenv("PATHFINDER_CONTENT")
	if root == "" {
		return nil, nil
	}
	return content.Load(root)
})

func requireRealWorld(t testing.TB) *world.World {
	t.Helper()
	started := time.Now()
	w, err := realWorld()
	if err != nil {
		t.Fatal(err)
	}
	if w == nil {
		t.Skip("set PATHFINDER_CONTENT to a content checkout to run real-content checks")
		return nil
	}
	t.Logf("%d maps, %d tiles, %d transitions (load %s)", len(w.Maps), w.States, w.Transitions(), time.Since(started))
	return w
}

func TestRealContentTravelsBetweenTownsAndFarms(t *testing.T) {
	w := requireRealWorld(t)
	e := search.NewEngine(w, 4)
	for _, tc := range []struct {
		name     string
		from     string
		x, y     int32
		goalMap  string
		maxSteps int
	}{
		// Asteria inn to the Strakewood Island farm circuit.
		{"inn to farm", "/shattered_islands/world_6_48_1", 1, 12, "/shattered_islands/world_1_50", 400},
		// Strakewood town to the same farm, the long way the bot once took.
		{"town to farm", "/shattered_islands/world_0_68", 12, 12, "/shattered_islands/world_1_50", 1200},
	} {
		start, ok := w.Lookup(tc.from)
		if !ok {
			t.Fatalf("%s: no map %s", tc.name, tc.from)
		}
		goal, ok := w.Lookup(tc.goalMap)
		if !ok {
			t.Fatalf("%s: no map %s", tc.name, tc.goalMap)
		}
		// Start from the nearest walkable square to the authored point.
		sx, sy, found := nearestWalkable(start, tc.x, tc.y)
		if !found {
			t.Fatalf("%s: nothing walkable on %s", tc.name, tc.from)
		}
		r, err := e.FindPath(context.Background(), search.Query{Start: search.Position{Map: start, X: sx, Y: sy}, GoalMap: goal})
		if err != nil {
			t.Fatal(err)
		}
		if r.Status != search.Found || len(r.Steps) > tc.maxSteps {
			t.Fatalf("%s: %v with %d steps", tc.name, r.Status, len(r.Steps))
		}
		t.Logf("%s: %d steps, cost %d, %d expanded in %s", tc.name, len(r.Steps), r.Cost, r.Expanded, r.Duration)
	}
}

func nearestWalkable(m *world.Map, x, y int32) (int32, int32, bool) {
	for radius := int32(0); radius < max(m.Width, m.Height); radius++ {
		for dy := -radius; dy <= radius; dy++ {
			for dx := -radius; dx <= radius; dx++ {
				nx, ny := x+dx, y+dy
				if !m.InBounds(nx, ny) {
					continue
				}
				tile := m.Tile(nx, ny)
				if m.Terrain[tile]&world.TerrainWalk != 0 && !m.Blocked[tile] {
					return nx, ny, true
				}
			}
		}
	}
	return 0, 0, false
}

func BenchmarkRealContentInnToFarm(b *testing.B) {
	w := requireRealWorld(b)
	e := search.NewEngine(w, 4)
	start, _ := w.Lookup("/shattered_islands/world_6_48_1")
	goal, _ := w.Lookup("/shattered_islands/world_1_50")
	sx, sy, _ := nearestWalkable(start, 1, 12)
	b.ReportAllocs()
	for b.Loop() {
		if r, _ := e.FindPath(context.Background(), search.Query{Start: search.Position{Map: start, X: sx, Y: sy}, GoalMap: goal}); r.Status != search.Found {
			b.Fatal(r.Status)
		}
	}
}
