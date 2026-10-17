package search_test

import (
	"fmt"
	"sync"
	"testing"

	"github.com/atrinik/pathfinder/internal/content"
	"github.com/atrinik/pathfinder/internal/fixture"
	"github.com/atrinik/pathfinder/internal/search"
	"github.com/atrinik/pathfinder/internal/world"
)

func engine(t testing.TB, c *fixture.Content) (*search.Engine, *world.World) {
	t.Helper()
	w, err := content.Load(c.Write())
	if err != nil {
		t.Fatal(err)
	}
	return search.NewEngine(w), w
}

func at(t testing.TB, w *world.World, path string, x, y int32) search.Position {
	t.Helper()
	m, ok := w.Lookup(path)
	if !ok {
		t.Fatalf("no map %s", path)
	}
	return search.Position{Map: m, X: x, Y: y}
}

func find(t testing.TB, e *search.Engine, q search.Query) search.Result {
	t.Helper()
	r, err := e.FindPath(q)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func tiles(r search.Result) string {
	s := ""
	for _, step := range r.Steps {
		s += fmt.Sprintf("%s:%d,%d ", step.Map.Path, step.X, step.Y)
	}
	return s
}

func TestOpenGroundCostsChebyshevMoves(t *testing.T) {
	c := fixture.New(t)
	c.Map("/t/open", ".....", ".....", ".....", ".....")
	e, w := engine(t, c)
	goal := at(t, w, "/t/open", 4, 3)
	r := find(t, e, search.Query{Start: at(t, w, "/t/open", 0, 0), GoalTile: &goal})
	if r.Status != search.Found || r.Cost != 4 || len(r.Steps) != 5 {
		t.Fatalf("status %v cost %d path %s", r.Status, r.Cost, tiles(r))
	}
	if r.Steps[0].Kind != search.Start || r.Steps[1].Kind != search.Walk {
		t.Fatalf("kinds %+v", r.Steps)
	}
	// Without diagonals the same trip takes the Manhattan distance.
	r = find(t, e, search.Query{Start: at(t, w, "/t/open", 0, 0), GoalTile: &goal,
		Agent: search.Agent{Corners: search.NoDiagonals}})
	if r.Cost != 7 {
		t.Fatalf("orthogonal cost %d", r.Cost)
	}
}

func TestCornerPoliciesDecideDiagonalSqueezes(t *testing.T) {
	// The only way from top-left to bottom-right passes diagonally between
	// two walls that touch at a corner.
	c := fixture.New(t)
	c.Map("/t/squeeze", ".#", "#.")
	e, w := engine(t, c)
	goal := at(t, w, "/t/squeeze", 1, 1)
	start := at(t, w, "/t/squeeze", 0, 0)
	if r := find(t, e, search.Query{Start: start, GoalTile: &goal}); r.Status != search.NoPath {
		t.Fatalf("strict corners must refuse the squeeze: %v %s", r.Status, tiles(r))
	}
	r := find(t, e, search.Query{Start: start, GoalTile: &goal, Agent: search.Agent{Corners: search.CornersLoose}})
	if r.Status != search.Found || r.Cost != 1 {
		t.Fatalf("loose corners: %v cost %d", r.Status, r.Cost)
	}
}

func TestWallsWaterAndLocksDependOnTheAgent(t *testing.T) {
	c := fixture.New(t)
	c.Map("/t/moat",
		"..#..",
		"..#..",
		"..L..",
		"..~..",
		"..#..")
	e, w := engine(t, c)
	start, goal := at(t, w, "/t/moat", 0, 0), at(t, w, "/t/moat", 4, 0)
	if r := find(t, e, search.Query{Start: start, GoalTile: &goal}); r.Status != search.NoPath {
		t.Fatalf("a walker without the key is shut out: %s", tiles(r))
	}
	keyed := search.Agent{Keys: map[string]bool{"silver_key": true}}
	if r := find(t, e, search.Query{Start: start, GoalTile: &goal, Agent: keyed}); r.Status != search.Found {
		t.Fatalf("the key opens the locked door: %v", r.Status)
	}
	// A swimmer (water only) crosses at the water tile instead.
	swimmer := search.Agent{TerrainMask: world.TerrainWalk | 2}
	r := find(t, e, search.Query{Start: start, GoalTile: &goal, Agent: swimmer})
	if r.Status != search.Found {
		t.Fatalf("swimmer: %v", r.Status)
	}
	crossed := false
	for _, step := range r.Steps {
		if step.X == 2 && step.Y == 3 {
			crossed = true
		}
	}
	if !crossed {
		t.Fatalf("swimmer path %s should cross the water at (2,3)", tiles(r))
	}
}

func TestSeamsJoinTiledMapsUnlessTheAgentStaysOnItsMap(t *testing.T) {
	c := fixture.New(t)
	c.Map("/w/world_0_0", "...", "...", "...")
	c.Map("/w/world_1_0", "...", "...", "...")
	c.Map("/w/world_1_1", "...", "...", "...")
	e, w := engine(t, c)
	goal := at(t, w, "/w/world_1_0", 1, 1)
	start := at(t, w, "/w/world_0_0", 1, 1)
	r := find(t, e, search.Query{Start: start, GoalTile: &goal})
	if r.Status != search.Found || r.Cost != 3 {
		t.Fatalf("status %v cost %d %s", r.Status, r.Cost, tiles(r))
	}
	seams := 0
	for _, step := range r.Steps {
		if step.Kind == search.Seam {
			seams++
		}
	}
	if seams != 1 {
		t.Fatalf("one seam crossing expected: %s", tiles(r))
	}
	// A diagonal through the shared corner reaches the south-east map.
	corner := at(t, w, "/w/world_1_1", 0, 0)
	r = find(t, e, search.Query{Start: at(t, w, "/w/world_0_0", 2, 2), GoalTile: &corner,
		Agent: search.Agent{Corners: search.CornersLoose}})
	if r.Status != search.Found || r.Cost != 1 {
		t.Fatalf("corner seam: %v %d %s", r.Status, r.Cost, tiles(r))
	}
	if r := find(t, e, search.Query{Start: start, GoalTile: &goal, Agent: search.Agent{StayOnMap: true}}); r.Status != search.NoPath {
		t.Fatalf("stay_on_map must not cross seams: %v", r.Status)
	}
	avoid := search.Agent{Avoided: map[int32]bool{goal.Map.Index: true}}
	if r := find(t, e, search.Query{Start: start, GoalTile: &goal, Agent: avoid}); r.Status != search.Found {
		t.Fatal("the goal map is exempt from avoidance")
	}
}

func TestAppliedAndAutomaticExits(t *testing.T) {
	c := fixture.New(t)
	c.Map("/t/town", ".....", ".....").
		Object("portal", 4, 0, "slaying /t/inn", "hp 0", "sp 1").
		Object("teleporter", 2, 1, "slaying /t/cellar", "hp 1", "sp 0")
	c.Map("/t/inn", "..", "..")
	c.Map("/t/cellar", "..", "..")
	e, w := engine(t, c)
	start := at(t, w, "/t/town", 0, 0)

	inn := at(t, w, "/t/inn", 0, 1)
	r := find(t, e, search.Query{Start: start, GoalTile: &inn})
	if r.Status != search.Found || r.Cost != 5 {
		t.Fatalf("portal: %v cost %d %s", r.Status, r.Cost, tiles(r))
	}
	last := r.Steps[len(r.Steps)-1]
	before := r.Steps[len(r.Steps)-2]
	if last.Kind != search.ExitUsed || last.Automatic || before.X != 4 || before.Y != 0 {
		t.Fatalf("an applied exit is used from the exit tile: %+v %s", last, tiles(r))
	}

	cellar := at(t, w, "/t/cellar", 1, 0)
	r = find(t, e, search.Query{Start: start, GoalTile: &cellar})
	if r.Status != search.Found {
		t.Fatalf("teleporter: %v", r.Status)
	}
	last, before = r.Steps[len(r.Steps)-1], r.Steps[len(r.Steps)-2]
	if last.Kind != search.ExitUsed || !last.Automatic || before.X != 2 || before.Y != 1 || before.Kind != search.Walk {
		t.Fatalf("an automatic exit is triggered by stepping on it: %s", tiles(r))
	}
	// Nobody can stand on an automatic exit, so it cannot be a goal and the
	// walk to the far side must go around it.
	far := at(t, w, "/t/town", 4, 1)
	r = find(t, e, search.Query{Start: at(t, w, "/t/town", 0, 1), GoalTile: &far})
	for _, step := range r.Steps {
		if step.Map.Path == "/t/town" && step.X == 2 && step.Y == 1 {
			t.Fatalf("path stepped on the teleporter: %s", tiles(r))
		}
	}
	if r.Status != search.Found || r.Cost != 4 {
		t.Fatalf("detour: %v cost %d %s", r.Status, r.Cost, tiles(r))
	}
}

func TestGoalMapBlockedTilesAndLimits(t *testing.T) {
	c := fixture.New(t)
	c.Map("/w/world_0_0", "...", "...", "...")
	c.Map("/w/world_1_0", "...", "...", "...")
	e, w := engine(t, c)
	east, _ := w.Lookup("/w/world_1_0")
	r := find(t, e, search.Query{Start: at(t, w, "/w/world_0_0", 0, 1), GoalMap: east})
	if r.Status != search.Found || r.Steps[len(r.Steps)-1].Map != east || r.Cost != 3 {
		t.Fatalf("goal map: %v %d %s", r.Status, r.Cost, tiles(r))
	}
	// Blocking the whole east column of the start map closes the seam.
	origin, _ := w.Lookup("/w/world_0_0")
	blocked := map[int32]bool{}
	for y := int32(0); y < 3; y++ {
		blocked[w.State(origin, 2, y)] = true
	}
	r = find(t, e, search.Query{Start: at(t, w, "/w/world_0_0", 0, 1), GoalMap: east, Blocked: blocked})
	if r.Status != search.NoPath {
		t.Fatalf("blocked column: %v %s", r.Status, tiles(r))
	}
	r = find(t, e, search.Query{Start: at(t, w, "/w/world_0_0", 0, 1), GoalMap: east, MaxExpanded: 1})
	if r.Status != search.LimitReached {
		t.Fatalf("limit: %v", r.Status)
	}
	if _, err := e.FindPath(search.Query{Start: at(t, w, "/w/world_0_0", 0, 1)}); err == nil {
		t.Fatal("a query needs exactly one goal")
	}
}

func TestConcurrentQueriesShareTheEngineSafely(t *testing.T) {
	c := fixture.New(t)
	rows := make([]string, 40)
	for i := range rows {
		rows[i] = "........................................"
	}
	rows[20] = "#######################################."
	c.Map("/t/field", rows...)
	e, w := engine(t, c)
	goal := at(t, w, "/t/field", 0, 39)
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := e.FindPath(search.Query{Start: at(t, w, "/t/field", int32(i), 0), GoalTile: &goal})
			if err != nil || r.Status != search.Found {
				t.Errorf("query %d: %v %v", i, r.Status, err)
			}
		}()
	}
	wg.Wait()
}

func BenchmarkOpenField(b *testing.B) {
	c := fixture.New(b)
	rows := make([]string, 200)
	for y := range rows {
		row := make([]byte, 200)
		for x := range row {
			row[x] = '.'
			if x%10 == 5 && y%40 != 0 {
				row[x] = '#'
			}
		}
		rows[y] = string(row)
	}
	c.Map("/b/field", rows...)
	e, w := engine(b, c)
	start, goal := at(b, w, "/b/field", 0, 1), at(b, w, "/b/field", 199, 199)
	b.ReportAllocs()
	for b.Loop() {
		if r, _ := e.FindPath(search.Query{Start: start, GoalTile: &goal}); r.Status != search.Found {
			b.Fatal(r.Status)
		}
	}
}

func BenchmarkTiledWorld(b *testing.B) {
	c := fixture.New(b)
	row := "........................"
	rows := make([]string, 24)
	for i := range rows {
		rows[i] = row
	}
	for x := range 10 {
		for y := range 10 {
			c.Map(fmt.Sprintf("/b/world_%d_%d", x, y), rows...)
		}
	}
	e, w := engine(b, c)
	start, goal := at(b, w, "/b/world_0_0", 0, 0), at(b, w, "/b/world_9_9", 23, 23)
	b.ReportAllocs()
	for b.Loop() {
		if r, _ := e.FindPath(search.Query{Start: start, GoalTile: &goal}); r.Status != search.Found {
			b.Fatal(r.Status)
		}
	}
}
