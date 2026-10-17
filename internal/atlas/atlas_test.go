package atlas_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/atrinik/pathfinder/internal/atlas"
	"github.com/atrinik/pathfinder/internal/content"
	"github.com/atrinik/pathfinder/internal/fixture"
	"github.com/atrinik/pathfinder/internal/search"
	"github.com/atrinik/pathfinder/internal/world"
)

func build(t testing.TB, c *fixture.Content) (*atlas.Atlas, *world.World) {
	t.Helper()
	w, err := content.Load(c.Write())
	if err != nil {
		t.Fatal(err)
	}
	return atlas.Build(w), w
}

func at(t testing.TB, w *world.World, path string, x, y int32) atlas.Position {
	t.Helper()
	m, ok := w.Lookup(path)
	if !ok {
		t.Fatalf("no map %s", path)
	}
	return atlas.Position{Map: m, X: x, Y: y}
}

func plan(t testing.TB, a *atlas.Atlas, r atlas.Request) atlas.Plan {
	t.Helper()
	p, err := a.Plan(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

var kindNames = map[atlas.WaypointKind]string{
	atlas.Start: "start", atlas.Walk: "walk", atlas.UseExit: "exit",
	atlas.CrossSeam: "seam", atlas.Arrive: "arrive", atlas.Goal: "goal",
}

func describe(p atlas.Plan) string {
	var parts []string
	for _, wp := range p.Waypoints {
		parts = append(parts, fmt.Sprintf("%s %s:%d,%d", kindNames[wp.Kind], wp.Map.Path, wp.X, wp.Y))
	}
	return strings.Join(parts, " | ")
}

func kinds(p atlas.Plan) string {
	var parts []string
	for _, wp := range p.Waypoints {
		parts = append(parts, kindNames[wp.Kind])
	}
	return strings.Join(parts, " ")
}

func TestRoadsAreCheaperThanOpenGround(t *testing.T) {
	c := fixture.New(t)
	c.Map("/t/town", "=========", ".........")
	a, w := build(t, c)
	goal := at(t, w, "/t/town", 8, 1)
	p := plan(t, a, atlas.Request{Start: at(t, w, "/t/town", 0, 1), GoalTile: &goal})
	// Straight along the bottom: 8 plain steps, 24. Up onto the road, six
	// road steps and back down: 3 + 12 + 3 = 18.
	if !p.Found || p.Cost != 18 || p.Tiles != 8 {
		t.Fatalf("found %v cost %d tiles %d: %s", p.Found, p.Cost, p.Tiles, describe(p))
	}
	onRoad := false
	for _, wp := range p.Waypoints {
		onRoad = onRoad || wp.Y == 0
	}
	if !onRoad {
		t.Fatalf("plan keeps off the road: %s", describe(p))
	}
}

func TestPlansUseExitsAndSeams(t *testing.T) {
	c := fixture.New(t)
	c.Map("/w/world_0_0", "...", "...", "...")
	c.Map("/w/world_1_0", "...", "...", "...").
		Object("portal", 2, 2, "slaying /t/inn", "hp 1", "sp 0")
	c.Map("/t/inn", "...", "...")
	a, w := build(t, c)
	goal := at(t, w, "/t/inn", 1, 1)
	p := plan(t, a, atlas.Request{Start: at(t, w, "/w/world_0_0", 0, 1), GoalTile: &goal})
	if !p.Found || kinds(p) != "start seam arrive exit arrive goal" {
		t.Fatalf("found %v: %s", p.Found, describe(p))
	}
	exit := p.Waypoints[3]
	if exit.Map.Path != "/w/world_1_0" || exit.X != 2 || exit.Y != 2 || exit.Automatic {
		t.Fatalf("exit waypoint %+v", exit)
	}
	if arrival := p.Waypoints[4]; arrival.Map.Path != "/t/inn" || arrival.X != 1 || arrival.Y != 0 {
		t.Fatalf("arrival %s", describe(p))
	}

	// A map goal ends on arrival.
	inn, _ := w.Lookup("/t/inn")
	p = plan(t, a, atlas.Request{Start: at(t, w, "/w/world_0_0", 0, 1), GoalMap: inn})
	if !p.Found || kinds(p) != "start seam arrive exit goal" {
		t.Fatalf("map goal: %s", describe(p))
	}
	if last := p.Waypoints[len(p.Waypoints)-1]; last.Map != inn {
		t.Fatalf("map goal ends off the goal map: %s", describe(p))
	}
	// Already there.
	p = plan(t, a, atlas.Request{Start: at(t, w, "/t/inn", 0, 0), GoalMap: inn})
	if !p.Found || kinds(p) != "goal" {
		t.Fatalf("start on the goal map: %s", describe(p))
	}
}

func TestAutomaticExitsAreSteppedOnto(t *testing.T) {
	c := fixture.New(t)
	c.Map("/t/town", "#####", ".....", "#####").
		Object("teleporter", 4, 1, "slaying /t/cellar", "hp 0", "sp 0")
	c.Map("/t/cellar", "..", "..")
	a, w := build(t, c)
	cellar, _ := w.Lookup("/t/cellar")
	p := plan(t, a, atlas.Request{Start: at(t, w, "/t/town", 0, 1), GoalMap: cellar})
	if !p.Found || kinds(p) != "start exit goal" {
		t.Fatalf("found %v: %s", p.Found, describe(p))
	}
	if exit := p.Waypoints[1]; !exit.Automatic || exit.X != 4 {
		t.Fatalf("teleporter waypoint %+v", exit)
	}
	// Four steps, the last onto the teleporter, then the transition.
	if p.Cost != 4*atlas.PlainStep+atlas.Transition || p.Tiles != 5 {
		t.Fatalf("cost %d tiles %d", p.Cost, p.Tiles)
	}
	// Nobody can stop on it.
	goal := at(t, w, "/t/town", 4, 1)
	if p := plan(t, a, atlas.Request{Start: at(t, w, "/t/town", 0, 1), GoalTile: &goal}); p.Found {
		t.Fatalf("planned onto an automatic exit: %s", describe(p))
	}
}

func TestPlansFromExitTilesUseThemFirst(t *testing.T) {
	c := fixture.New(t)
	c.Map("/t/town", ".....").
		Object("teleporter", 2, 0, "slaying /t/cellar", "hp 0", "sp 0").
		Object("portal", 4, 0, "slaying /t/inn", "hp 0", "sp 0")
	c.Map("/t/cellar", "..")
	c.Map("/t/inn", "..")
	a, w := build(t, c)
	cellar, _ := w.Lookup("/t/cellar")
	inn, _ := w.Lookup("/t/inn")
	onTeleporter := at(t, w, "/t/town", 2, 0)
	// A walker on a teleporter is carried off at once.
	p := plan(t, a, atlas.Request{Start: onTeleporter, GoalMap: cellar})
	if !p.Found || kinds(p) != "start exit goal" || !p.Waypoints[1].Automatic || p.Waypoints[1].X != 2 {
		t.Fatalf("from the teleporter: %s", describe(p))
	}
	beside := at(t, w, "/t/town", 3, 0)
	for name, r := range map[string]atlas.Request{
		"walk away":       {Start: onTeleporter, GoalTile: &beside},
		"other exit":      {Start: onTeleporter, GoalMap: inn},
		"stay on the map": {Start: onTeleporter, GoalMap: onTeleporter.Map},
		"stand on it":     {Start: onTeleporter, GoalTile: &onTeleporter},
	} {
		if p := plan(t, a, r); p.Found {
			t.Errorf("%s: planned %s", name, describe(p))
		}
	}
	// On a portal, the start keeps its own waypoint before the exit.
	p = plan(t, a, atlas.Request{Start: at(t, w, "/t/town", 4, 0), GoalMap: inn})
	if !p.Found || kinds(p) != "start exit goal" || p.Waypoints[1].X != 4 || p.Waypoints[1].Automatic {
		t.Fatalf("from the portal: %s", describe(p))
	}
}

func TestSeamCrossingsFollowCornerAndExitRules(t *testing.T) {
	// Diagonal neighbours touch only at a corner: a strict-corner walker
	// needs both orthogonal neighbours to step across.
	c := fixture.New(t)
	c.Map("/w/world_0_0", "...", "...", "...")
	c.Map("/w/world_1_1", "...", "...", "...")
	a, w := build(t, c)
	far, _ := w.Lookup("/w/world_1_1")
	if p := plan(t, a, atlas.Request{Start: at(t, w, "/w/world_0_0", 1, 1), GoalMap: far}); p.Found {
		t.Fatalf("crossed a bare corner: %s", describe(p))
	}
	c.Map("/w/world_1_0", "...", "...", "...")
	c.Map("/w/world_0_1", "...", "...", "...")
	a, w = build(t, c)
	far, _ = w.Lookup("/w/world_1_1")
	if p := plan(t, a, atlas.Request{Start: at(t, w, "/w/world_0_0", 1, 1), GoalMap: far}); !p.Found {
		t.Fatal("no route once the corner is open")
	}

	// Stepping across a seam onto a teleporter takes it.
	c = fixture.New(t)
	c.Map("/w/world_0_0", "...")
	c.Map("/w/world_1_0", "#").Object("teleporter", 0, 0, "slaying /t/cellar", "hp 0", "sp 0")
	c.Map("/t/cellar", "..")
	a, w = build(t, c)
	cellar, _ := w.Lookup("/t/cellar")
	p := plan(t, a, atlas.Request{Start: at(t, w, "/w/world_0_0", 0, 0), GoalMap: cellar})
	if p.Found {
		t.Fatalf("crossed onto a teleporter in a wall: %s", describe(p))
	}
	c.Map("/w/world_1_0", ".").Object("teleporter", 0, 0, "slaying /t/cellar", "hp 0", "sp 0")
	a, w = build(t, c)
	cellar, _ = w.Lookup("/t/cellar")
	p = plan(t, a, atlas.Request{Start: at(t, w, "/w/world_0_0", 0, 0), GoalMap: cellar})
	if !p.Found || kinds(p) != "start seam exit goal" || !p.Waypoints[2].Automatic {
		t.Fatalf("across onto the teleporter: %s", describe(p))
	}
}

func TestWaypointsKeepRoadDetours(t *testing.T) {
	c := fixture.New(t)
	c.Map("/t/town", "=====", ".....")
	a, w := build(t, c)
	goal := at(t, w, "/t/town", 4, 1)
	p := plan(t, a, atlas.Request{Start: at(t, w, "/t/town", 0, 1), GoalTile: &goal})
	// The road costs 10 against 12 straight across, and the waypoints must
	// put the walker on it.
	if !p.Found || p.Cost != 10 || describe(p) != "start /t/town:0,1 | walk /t/town:1,0 | walk /t/town:3,0 | goal /t/town:4,1" {
		t.Fatalf("cost %d: %s", p.Cost, describe(p))
	}
}

func TestWaypointsStayWithinSpacingAndKeepCorners(t *testing.T) {
	// An L-shaped corridor: 20 tiles east, then 10 south.
	rows := []string{strings.Repeat(".", 21)}
	for range 10 {
		rows = append(rows, strings.Repeat("#", 20)+".")
	}
	c := fixture.New(t)
	c.Map("/t/hall", rows...)
	a, w := build(t, c)
	goal := at(t, w, "/t/hall", 20, 10)
	for _, spacing := range []int32{0, 3, 7, 50} {
		p := plan(t, a, atlas.Request{Start: at(t, w, "/t/hall", 0, 0), GoalTile: &goal, Spacing: spacing})
		if !p.Found || p.Tiles != 30 {
			t.Fatalf("spacing %d: found %v tiles %d", spacing, p.Found, p.Tiles)
		}
		limit := spacing
		if limit == 0 {
			limit = atlas.DefaultSpacing
		}
		corner := false
		for i := 1; i < len(p.Waypoints); i++ {
			prev, cur := p.Waypoints[i-1], p.Waypoints[i]
			if d := max(abs(cur.X-prev.X), abs(cur.Y-prev.Y)); d > limit {
				t.Fatalf("spacing %d: waypoints %d apart: %s", spacing, d, describe(p))
			}
			corner = corner || (cur.X == 20 && cur.Y == 0)
		}
		if !corner {
			t.Fatalf("spacing %d: the corner was cut: %s", spacing, describe(p))
		}
		if spacing == 50 && len(p.Waypoints) != 3 {
			t.Fatalf("a wide spacing keeps only the corner: %s", describe(p))
		}
	}
}

func TestAvoidedMapsAndUnreachableGoals(t *testing.T) {
	c := fixture.New(t)
	c.Map("/t/start", "...").
		Object("portal", 0, 0, "slaying /t/short", "hp 0", "sp 0").
		Object("portal", 2, 0, "slaying /t/long", "hp 0", "sp 0")
	c.Map("/t/short", "..").Object("portal", 1, 0, "slaying /t/goal", "hp 0", "sp 0")
	c.Map("/t/long", "......").Object("portal", 5, 0, "slaying /t/goal", "hp 0", "sp 0")
	c.Map("/t/goal", "...", "###", "...")
	a, w := build(t, c)
	short, _ := w.Lookup("/t/short")
	goalMap, _ := w.Lookup("/t/goal")
	start := at(t, w, "/t/start", 1, 0)
	via := func(p atlas.Plan, path string) bool {
		for _, wp := range p.Waypoints {
			if wp.Map.Path == path {
				return true
			}
		}
		return false
	}
	if p := plan(t, a, atlas.Request{Start: start, GoalMap: goalMap}); !p.Found || !via(p, "/t/short") {
		t.Fatalf("shortest: %s", describe(p))
	}
	p := plan(t, a, atlas.Request{Start: start, GoalMap: goalMap, Avoided: map[int32]bool{short.Index: true}})
	if !p.Found || via(p, "/t/short") || !via(p, "/t/long") {
		t.Fatalf("avoiding /t/short: %s", describe(p))
	}
	// The goal map is exempt from avoidance.
	if p := plan(t, a, atlas.Request{Start: start, GoalMap: goalMap, Avoided: map[int32]bool{goalMap.Index: true}}); !p.Found {
		t.Fatal("the goal map was avoided")
	}
	// The wall row cuts the goal map in two.
	walled := at(t, w, "/t/goal", 1, 2)
	if p := plan(t, a, atlas.Request{Start: start, GoalTile: &walled}); p.Found {
		t.Fatalf("planned through a wall: %s", describe(p))
	}
}

func TestInvalidRequests(t *testing.T) {
	c := fixture.New(t)
	c.Map("/t/a", "..")
	a, w := build(t, c)
	m, _ := w.Lookup("/t/a")
	goal := at(t, w, "/t/a", 1, 0)
	outside := at(t, w, "/t/a", 5, 0)
	for name, r := range map[string]atlas.Request{
		"no start":         {GoalMap: m},
		"start outside":    {Start: outside, GoalMap: m},
		"no goal":          {Start: goal},
		"both goals":       {Start: goal, GoalMap: m, GoalTile: &goal},
		"goal outside":     {Start: goal, GoalTile: &outside},
		"goal tile no map": {Start: goal, GoalTile: &atlas.Position{X: 1}},
	} {
		if _, err := a.Plan(context.Background(), r); err != atlas.ErrInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	other := at(t, w, "/t/a", 0, 0)
	if _, err := a.Plan(ctx, atlas.Request{Start: other, GoalTile: &goal}); err == nil {
		t.Fatal("a cancelled plan succeeded")
	}
}

// TestPlansMatchExactSearchWithoutRoads checks, on random road-free worlds,
// that the planner finds a route exactly when the precise search does, at
// the same cost: without roads and seams the connector graph loses nothing.
func TestPlansMatchExactSearchWithoutRoads(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 11))
	for trial := range 300 {
		c := fixture.New(t)
		const maps = 4
		sizes := make([][2]int, maps)
		for i := range maps {
			width, height := 3+rng.IntN(6), 3+rng.IntN(6)
			sizes[i] = [2]int{width, height}
			var rows []string
			for range height {
				var b strings.Builder
				for range width {
					if rng.IntN(4) == 0 {
						b.WriteByte('#')
					} else {
						b.WriteByte('.')
					}
				}
				rows = append(rows, b.String())
			}
			m := c.Map(fmt.Sprintf("/r/m%d", i), rows...)
			for range 1 + rng.IntN(3) {
				to := rng.IntN(maps)
				arch := "portal"
				if rng.IntN(3) == 0 {
					arch = "teleporter"
				}
				dw, dh := sizes[to][0], sizes[to][1]
				if dw == 0 {
					dw, dh = 3, 3 // the destination is drawn later; stay inside its smallest size
				}
				m.Object(arch, rng.IntN(width), rng.IntN(height),
					fmt.Sprintf("slaying /r/m%d", to), fmt.Sprintf("hp %d", rng.IntN(dw)), fmt.Sprintf("sp %d", rng.IntN(dh)))
			}
		}
		a, w := build(t, c)
		e := search.NewEngine(w, 1)
		for range 20 {
			sm := w.Maps[rng.IntN(len(w.Maps))]
			gm := w.Maps[rng.IntN(len(w.Maps))]
			sx, sy := int32(rng.IntN(int(sm.Width))), int32(rng.IntN(int(sm.Height)))
			gx, gy := int32(rng.IntN(int(gm.Width))), int32(rng.IntN(int(gm.Height)))
			if tile := sm.Tile(sx, sy); sm.Terrain[tile]&world.TerrainWalk == 0 || sm.Blocked[tile] {
				continue // starts on passable automatic exits are kept
			}
			goal := atlas.Position{Map: gm, X: gx, Y: gy}
			p := plan(t, a, atlas.Request{Start: atlas.Position{Map: sm, X: sx, Y: sy}, GoalTile: &goal})
			r, err := e.FindPath(context.Background(), search.Query{
				Start: search.Position{Map: sm, X: sx, Y: sy}, GoalTile: &search.Position{Map: gm, X: gx, Y: gy},
			})
			if err != nil {
				t.Fatal(err)
			}
			if p.Found != (r.Status == search.Found) || (p.Found && p.Cost != 3*r.Cost) {
				t.Fatalf("trial %d %s:%d,%d -> %s:%d,%d: plan %v cost %d (%s), search %v cost %d\n%s",
					trial, sm.Path, sx, sy, gm.Path, gx, gy, p.Found, p.Cost, describe(p), r.Status, r.Cost, draw(w))
			}
			if p.Found {
				checkWalkable(t, p)
			}
			p = plan(t, a, atlas.Request{Start: atlas.Position{Map: sm, X: sx, Y: sy}, GoalMap: gm})
			r, err = e.FindPath(context.Background(), search.Query{Start: search.Position{Map: sm, X: sx, Y: sy}, GoalMap: gm})
			if err != nil {
				t.Fatal(err)
			}
			if p.Found != (r.Status == search.Found) || (p.Found && p.Cost != 3*r.Cost) {
				t.Fatalf("trial %d %s:%d,%d -> map %s: plan %v cost %d (%s), search %v cost %d\n%s",
					trial, sm.Path, sx, sy, gm.Path, p.Found, p.Cost, describe(p), r.Status, r.Cost, draw(w))
			}
		}
	}
}

func blocked(m *world.Map, x, y int32) bool {
	tile := m.Tile(x, y)
	if m.Terrain[tile]&world.TerrainWalk == 0 || m.Blocked[tile] {
		return true
	}
	for _, exit := range m.Exits[tile] {
		if exit.Automatic {
			return true
		}
	}
	return false
}

// checkWalkable asserts every intermediate waypoint stands on open ground.
func checkWalkable(t *testing.T, p atlas.Plan) {
	t.Helper()
	for i, wp := range p.Waypoints {
		if wp.Kind == atlas.Walk || wp.Kind == atlas.Goal || wp.Kind == atlas.Arrive {
			if blocked(wp.Map, wp.X, wp.Y) {
				t.Fatalf("waypoint %d is not standable: %s", i, describe(p))
			}
		}
	}
}

func TestStatsCountTheNetwork(t *testing.T) {
	c := fixture.New(t)
	c.Map("/t/a", "==.").Object("portal", 2, 0, "slaying /t/b", "hp 0", "sp 0")
	c.Map("/t/b", "..").Object("portal", 1, 0, "slaying /t/a", "hp 0", "sp 0")
	a, _ := build(t, c)
	s := a.Stats()
	// Connectors: both portals and both arrivals. Edges: two exits and a
	// walk each way between each map's portal and arrival.
	if s.Connectors != 4 || s.Edges != 6 || s.RoadTiles != 2 {
		t.Fatalf("%+v", s)
	}
}

func abs(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

// draw renders a world for failure messages.
func draw(w *world.World) string {
	var b strings.Builder
	for _, m := range w.Maps {
		fmt.Fprintf(&b, "%s\n", m.Path)
		for y := range m.Height {
			for x := range m.Width {
				tile := m.Tile(x, y)
				// Exits are T (automatic) or P, lower case when blocked.
				ch := byte('.')
				switch {
				case len(m.Exits[tile]) > 0 && m.Exits[tile][0].Automatic:
					ch = 'T'
				case len(m.Exits[tile]) > 0:
					ch = 'P'
				case m.Blocked[tile]:
					ch = '#'
				}
				if ch != '#' && ch != '.' && m.Blocked[tile] {
					ch += 'a' - 'A'
				}
				b.WriteByte(ch)
			}
			b.WriteByte('\n')
		}
		for tile, exits := range m.Exits {
			for _, e := range exits {
				fmt.Fprintf(&b, "  %d,%d -> %s:%d,%d auto=%v\n", tile%m.Width, tile/m.Width, w.Maps[e.To].Path, e.X, e.Y, e.Automatic)
			}
		}
	}
	return b.String()
}
