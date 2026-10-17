package content_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atrinik/pathfinder/internal/content"
	"github.com/atrinik/pathfinder/internal/fixture"
	"github.com/atrinik/pathfinder/internal/world"
)

func TestParseArchetypesKeepsMultipartPartsAndSkipsText(t *testing.T) {
	archetypes, err := content.ParseArchetypes(strings.NewReader(fixture.Archetypes))
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*content.Archetype{}
	for _, a := range archetypes {
		byName[a.Name] = a
	}
	wall := byName["long_wall"]
	if wall == nil || len(wall.Parts) != 1 || wall.Parts[0].DX != 1 || wall.Parts[0].DY != 0 {
		t.Fatalf("long_wall parts = %+v", wall)
	}
	if _, ok := byName["long_wall_b"]; ok {
		t.Fatal("a More part must not become its own archetype")
	}
	if wall.Attrs["msg"] != "" || wall.Attrs["A"] != "" {
		t.Fatalf("msg text leaked into attributes: %v", wall.Attrs)
	}
}

func TestParseMapReadsHeaderObjectsAndInventory(t *testing.T) {
	text := "arch map\nwidth 3\nheight 2\nmsg\nhello\nendmsg\nend\n" +
		"arch chest\nx 1\ny 1\narch sword\nend\nend\n"
	file, ok, err := content.ParseMap(strings.NewReader(text))
	if err != nil || !ok {
		t.Fatalf("ParseMap = %v, %v", ok, err)
	}
	if file.Header.Int("width", 0) != 3 || len(file.Objects) != 1 {
		t.Fatalf("header %v, objects %d", file.Header, len(file.Objects))
	}
	chest := file.Objects[0]
	if chest.Arch != "chest" || len(chest.Children) != 1 || chest.Children[0].Arch != "sword" {
		t.Fatalf("chest = %+v", chest)
	}
	if _, ok, _ := content.ParseMap(strings.NewReader("print('a script')\n")); ok {
		t.Fatal("a non-map file must not parse as a map")
	}
}

func TestCoordinateNamesFollowTheTiledWorldConvention(t *testing.T) {
	for _, tc := range []struct {
		path      string
		prefix    string
		x, y, z   int
		available bool
	}{
		{"/shattered_islands/world_1_50", "world", 1, 50, 0, true},
		{"/shattered_islands/world_6_48_-2", "world", 6, 48, -2, true},
		{"/shattered_islands/world_-2_57", "world", -2, 57, 0, true},
		{"/shattered_islands/strakewood_island/apartments/apartment_cheap", "", 0, 0, 0, false},
		{"/shattered_islands/strakewood_island/rockforge/rockforge_b_0104", "", 0, 0, 0, false},
		// The integers must end the name: these are not coordinate maps.
		{"/zone/zone_1_variant_2", "", 0, 0, 0, false},
		{"/zone/1_2", "", 0, 0, 0, false},
		{"/zone/level_1_2_3_4", "", 0, 0, 0, false},
	} {
		prefix, x, y, z, ok := content.Coordinate(tc.path)
		if ok != tc.available || (ok && (prefix != tc.prefix || x != tc.x || y != tc.y || z != tc.z)) {
			t.Errorf("Coordinate(%s) = %q %d %d %d %v", tc.path, prefix, x, y, z, ok)
		}
	}
	if next, _ := content.CoordinateNeighbour("/a/world_6_48_1", 0, 0, -1); next != "/a/world_6_48" {
		t.Errorf("down from level 1 = %s", next)
	}
	if got := content.Resolve("/a/b/c", "../d/e"); got != "/a/d/e" {
		t.Errorf("Resolve = %s", got)
	}
}

func load(t *testing.T, c *fixture.Content) *world.World {
	t.Helper()
	w, err := content.Load(c.Write())
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestCompileTerrainBlockersLocksAndMultipartFootprints(t *testing.T) {
	c := fixture.New(t)
	c.Map("/test/room", ".#D", "L~.", "...").Object("long_wall", 0, 2)
	w := load(t, c)
	m, _ := w.Lookup("/test/room")
	check := func(x, y int32, terrain uint32, blocked bool) {
		t.Helper()
		tile := m.Tile(x, y)
		if m.Terrain[tile] != terrain || m.Blocked[tile] != blocked {
			t.Errorf("(%d,%d) terrain %d blocked %v", x, y, m.Terrain[tile], m.Blocked[tile])
		}
	}
	check(0, 0, 1, false)
	check(1, 0, 1, true)  // wall
	check(2, 0, 1, false) // a closed door is no_pass but openable
	check(1, 1, 2, false) // water
	check(0, 2, 1, true)  // multipart head
	check(1, 2, 1, true)  // its More part
	if m.Locks[m.Tile(0, 1)] != "silver_key" {
		t.Errorf("locks = %v", m.Locks)
	}
}

func TestCompileLinksSeamsExitsAndStairs(t *testing.T) {
	c := fixture.New(t)
	c.Map("/w/world_0_0", "...", "...", "...").
		Object("portal", 1, 1, "slaying /inn/hall", "hp 1", "sp 0").
		Object("stairs_up", 2, 2)
	c.Map("/w/world_1_0", "...", "...", "...")
	c.Map("/w/world_0_0_1", "...", "...", "...")
	c.Map("/inn/hall", "..", "..").Set("tile_path_2", "/w/world_0_0").
		Object("teleporter", 0, 0, "slaying ../w/world_1_0")
	w := load(t, c)
	origin, _ := w.Lookup("/w/world_0_0")
	east, _ := w.Lookup("/w/world_1_0")
	hall, _ := w.Lookup("/inn/hall")
	upper, _ := w.Lookup("/w/world_0_0_1")
	if origin.Seams[world.East] != east.Index || east.Seams[world.West] != origin.Index {
		t.Errorf("coordinate seams: %v %v", origin.Seams, east.Seams)
	}
	if origin.Seams[world.South] != -1 {
		t.Errorf("no south neighbour exists: %v", origin.Seams)
	}
	if hall.Seams[world.East] != origin.Index {
		t.Errorf("declared tile_path_2: %v", hall.Seams)
	}
	portal := origin.Exits[origin.Tile(1, 1)]
	if len(portal) != 1 || portal[0].To != hall.Index || portal[0].X != 1 || portal[0].Automatic {
		t.Errorf("portal = %+v", portal)
	}
	stairs := origin.Exits[origin.Tile(2, 2)]
	if len(stairs) != 1 || stairs[0].To != upper.Index || stairs[0].X != 2 || stairs[0].Y != 2 {
		t.Errorf("stairs = %+v", stairs)
	}
	teleporter := hall.Exits[hall.Tile(0, 0)]
	if len(teleporter) != 1 || teleporter[0].To != east.Index || !teleporter[0].Automatic {
		t.Errorf("teleporter = %+v", teleporter)
	}
	// hp/sp of -1 (absent) arrive at the destination's entry point.
	if teleporter[0].X != east.EnterX || teleporter[0].Y != east.EnterY {
		t.Errorf("teleporter arrival = %+v", teleporter[0])
	}
}

func TestLoadRejectsExitsArrivingOutsideTheirDestination(t *testing.T) {
	c := fixture.New(t)
	c.Map("/t/town", "...").Object("portal", 0, 0, "slaying /t/inn", "hp 5", "sp 0")
	c.Map("/t/inn", "..")
	_, err := content.Load(c.Write())
	if err == nil || !strings.Contains(err.Error(), "/t/town") || !strings.Contains(err.Error(), "outside /t/inn") {
		t.Fatalf("got %v", err)
	}
}

func TestMultipartDoorsLockEverySquare(t *testing.T) {
	c := fixture.New(t)
	c.Map("/t/gate", "...", "...").Object("double_door", 0, 1)
	root := c.Write()
	extra := "Object double_door\nname double door\ntype 20\nno_pass 1\nslaying gate_key\nend\nMore\n" +
		"Object double_door_b\ntype 20\nno_pass 1\nx 1\nend\n"
	if err := os.WriteFile(filepath.Join(root, "arch", "double_door.arc"), []byte(extra), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := content.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := w.Lookup("/t/gate")
	if m.Locks[m.Tile(0, 1)] != "gate_key" || m.Locks[m.Tile(1, 1)] != "gate_key" {
		t.Fatalf("locks = %v", m.Locks)
	}
	if m.Blocked[m.Tile(1, 1)] {
		t.Fatal("a door part is passable with its key")
	}
}

func TestMultipartRoadsPaveEverySquare(t *testing.T) {
	c := fixture.New(t)
	c.Map("/t/river", "~~~", "~~~").Object("long_bridge", 0, 1)
	root := c.Write()
	bridges := filepath.Join(root, "arch", "floors", "bridge_wood")
	if err := os.MkdirAll(bridges, 0o755); err != nil {
		t.Fatal(err)
	}
	extra := "Object long_bridge\nname bridge\nterrain_type 1\nend\nMore\n" +
		"Object long_bridge_b\nterrain_type 1\nx 1\nend\n"
	if err := os.WriteFile(filepath.Join(bridges, "long_bridge.arc"), []byte(extra), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := content.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := w.Lookup("/t/river")
	if !m.Road[m.Tile(0, 1)] || !m.Road[m.Tile(1, 1)] || m.Road[m.Tile(2, 1)] || m.Road[m.Tile(0, 0)] {
		t.Fatalf("road = %v", m.Road)
	}
}

func TestLoadRejectsMapsWithoutAValidSize(t *testing.T) {
	for _, header := range []string{"height 3\n", "width 0\nheight 3\n", "width x\nheight 3\n", "width 99999\nheight 3\n"} {
		c := fixture.New(t)
		root := c.Write()
		dir := filepath.Join(root, "maps", "broken")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "map"), []byte("arch map\n"+header+"end\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := content.Load(root)
		if err == nil || !strings.Contains(err.Error(), "/broken/map") {
			t.Errorf("header %q: %v", header, err)
		}
	}
}

func TestLoadDigestCoversEveryLoadedFile(t *testing.T) {
	c := fixture.New(t)
	c.Map("/test/a", "..")
	root := c.Write()
	first, err := content.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "maps", "test", "a.py"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, _ := content.Load(root)
	if first.Digest != second.Digest {
		t.Error("files with an extension are not maps and must not change the digest")
	}
	c.Map("/test/b", "..")
	c.Write()
	third, _ := content.Load(root)
	if third.Digest == first.Digest || len(third.Maps) != 2 {
		t.Error("a new map must change the digest")
	}
}
