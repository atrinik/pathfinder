package atlas_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/atrinik/pathfinder/internal/atlas"
	"github.com/atrinik/pathfinder/internal/content"
	"github.com/atrinik/pathfinder/internal/search"
	"github.com/atrinik/pathfinder/internal/world"
)

// realAtlas loads and builds the content checkout named by
// PATHFINDER_CONTENT once.
var realAtlas = sync.OnceValues(func() (*atlas.Atlas, error) {
	root := os.Getenv("PATHFINDER_CONTENT")
	if root == "" {
		return nil, nil
	}
	w, err := content.Load(root)
	if err != nil {
		return nil, err
	}
	return atlas.Build(w), nil
})

func requireRealAtlas(t testing.TB) *atlas.Atlas {
	t.Helper()
	started := time.Now()
	a, err := realAtlas()
	if err != nil {
		t.Fatal(err)
	}
	if a == nil {
		t.Skip("set PATHFINDER_CONTENT to a content checkout to run real-content checks")
		return nil
	}
	s := a.Stats()
	t.Logf("%d maps: %d connectors, %d edges, %d road tiles (build %s, load and build %s)",
		len(a.World.Maps), s.Connectors, s.Edges, s.RoadTiles, a.BuildTime, time.Since(started))
	return a
}

var realJourneys = []struct {
	name    string
	from    string
	x, y    int32
	goalMap string
}{
	// Asteria inn to the Strakewood Island farm circuit.
	{"inn to farm", "/shattered_islands/world_6_48_1", 1, 12, "/shattered_islands/world_1_50"},
	// Strakewood town to the same farm.
	{"town to farm", "/shattered_islands/world_0_68", 12, 12, "/shattered_islands/world_1_50"},
}

func TestRealContentPlansFollowTheExactRoutesClosely(t *testing.T) {
	a := requireRealAtlas(t)
	e := search.NewEngine(a.World, 1)
	for _, tc := range realJourneys {
		start, goal := lookup(t, a.World, tc.from), lookup(t, a.World, tc.goalMap)
		sx, sy, ok := nearestStandable(start, tc.x, tc.y)
		if !ok {
			t.Fatalf("%s: nothing standable on %s", tc.name, tc.from)
		}
		p, err := a.Plan(context.Background(), atlas.Request{Start: atlas.Position{Map: start, X: sx, Y: sy}, GoalMap: goal})
		if err != nil {
			t.Fatal(err)
		}
		r, err := e.FindPath(context.Background(), search.Query{Start: search.Position{Map: start, X: sx, Y: sy}, GoalMap: goal})
		if err != nil {
			t.Fatal(err)
		}
		if !p.Found || r.Status != search.Found {
			t.Fatalf("%s: plan %v, search %v", tc.name, p.Found, r.Status)
		}
		// Roads may lengthen the walk, but not by more than half.
		if p.Tiles > r.Cost*3/2 {
			t.Fatalf("%s: plan walks %d tiles, the shortest route %d", tc.name, p.Tiles, r.Cost)
		}
		checkWalkable(t, p)
		t.Logf("%s: %d waypoints over %d tiles (shortest %d), %d connectors expanded in %s; exact search %s",
			tc.name, len(p.Waypoints), p.Tiles, r.Cost, p.Expanded, p.Duration, r.Duration)
	}
}

func lookup(t testing.TB, w *world.World, path string) *world.Map {
	t.Helper()
	m, ok := w.Lookup(path)
	if !ok {
		t.Fatalf("no map %s", path)
	}
	return m
}

func nearestStandable(m *world.Map, x, y int32) (int32, int32, bool) {
	for radius := int32(0); radius < max(m.Width, m.Height); radius++ {
		for dy := -radius; dy <= radius; dy++ {
			for dx := -radius; dx <= radius; dx++ {
				nx, ny := x+dx, y+dy
				if m.InBounds(nx, ny) && !blocked(m, nx, ny) {
					if _, locked := m.Locks[m.Tile(nx, ny)]; !locked {
						return nx, ny, true
					}
				}
			}
		}
	}
	return 0, 0, false
}

func BenchmarkRealContentPlanTownToFarm(b *testing.B) {
	a := requireRealAtlas(b)
	tc := realJourneys[1]
	start, goal := lookup(b, a.World, tc.from), lookup(b, a.World, tc.goalMap)
	sx, sy, _ := nearestStandable(start, tc.x, tc.y)
	b.ReportAllocs()
	for b.Loop() {
		if p, _ := a.Plan(context.Background(), atlas.Request{Start: atlas.Position{Map: start, X: sx, Y: sy}, GoalMap: goal}); !p.Found {
			b.Fatal("no plan")
		}
	}
}
