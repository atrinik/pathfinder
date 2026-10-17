package atlas_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	atlasv1 "github.com/atrinik/pathfinder/gen/go/atrinik/pathfinder/atlas/v1"
	"github.com/atrinik/pathfinder/internal/atlas"
	"github.com/atrinik/pathfinder/internal/content"
	"github.com/atrinik/pathfinder/internal/fixture"
	"github.com/atrinik/pathfinder/internal/world"
	"google.golang.org/protobuf/proto"
)

func fileWorld(t *testing.T) (*fixture.Content, *world.World) {
	t.Helper()
	c := fixture.New(t)
	c.Map("/w/world_0_0", "=..", "=#.", "...").
		Object("portal", 2, 2, "slaying /t/inn", "hp 0", "sp 0")
	c.Map("/w/world_1_0", "...", "...", "...").
		Object("teleporter", 1, 1, "slaying /t/inn", "hp 1", "sp 1")
	c.Map("/t/inn", "...", "...").
		Object("portal", 0, 0, "slaying /w/world_0_0", "hp 0", "sp 0")
	w, err := content.Load(c.Write())
	if err != nil {
		t.Fatal(err)
	}
	return c, w
}

func TestAtlasFilesRoundTripExactly(t *testing.T) {
	_, w := fileWorld(t)
	built := atlas.Build(w)
	dir := t.TempDir()
	first, second := filepath.Join(dir, "a.atlas"), filepath.Join(dir, "b.atlas")
	if err := built.Save(first); err != nil {
		t.Fatal(err)
	}
	if err := atlas.Build(w).Save(second); err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(first)
	b, _ := os.ReadFile(second)
	if !bytes.Equal(a, b) {
		t.Fatal("two builds of the same content wrote different atlas files")
	}
	loaded, err := atlas.Load(w, first)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Loaded || built.Loaded {
		t.Fatalf("loaded flags: built %v, loaded %v", built.Loaded, loaded.Loaded)
	}
	if !reflect.DeepEqual(built.Connectors, loaded.Connectors) || !reflect.DeepEqual(built.Out, loaded.Out) {
		t.Fatal("the loaded graph differs from the built one")
	}
	inn, _ := w.Lookup("/t/inn")
	start := at(t, w, "/w/world_1_0", 2, 2)
	want := plan(t, built, atlas.Request{Start: start, GoalMap: inn})
	got := plan(t, loaded, atlas.Request{Start: start, GoalMap: inn})
	if !want.Found || describe(want) != describe(got) || want.Cost != got.Cost {
		t.Fatalf("plans differ:\n built %s\nloaded %s", describe(want), describe(got))
	}
}

func TestAtlasFilesFromOtherContentAreRefused(t *testing.T) {
	_, w := fileWorld(t)
	path := filepath.Join(t.TempDir(), "w.atlas")
	if err := atlas.Build(w).Save(path); err != nil {
		t.Fatal(err)
	}
	c := fixture.New(t)
	c.Map("/t/other", "..")
	other, err := content.Load(c.Write())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := atlas.Load(other, path); !errors.Is(err, atlas.ErrMismatch) {
		t.Fatalf("other content: %v", err)
	}
	for name, mutate := range map[string]func(*atlasv1.Atlas){
		"format":   func(f *atlasv1.Atlas) { f.Format = 2 },
		"map list": func(f *atlasv1.Atlas) { f.Maps[0] = "/t/elsewhere" },
		"maps":     func(f *atlasv1.Atlas) { f.Maps = f.Maps[1:] },
	} {
		f := atlas.Build(w).Encode()
		mutate(f)
		if _, err := atlas.Decode(w, f); !errors.Is(err, atlas.ErrMismatch) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestMalformedAtlasFilesAreRejected(t *testing.T) {
	_, w := fileWorld(t)
	for name, mutate := range map[string]func(*atlasv1.Atlas){
		"map index":     func(f *atlasv1.Atlas) { f.Connectors[0].Map = 99 },
		"out of bounds": func(f *atlasv1.Atlas) { f.Connectors[0].X = 40 },
		"no kinds":      func(f *atlasv1.Atlas) { f.Connectors[0].Kinds = 0 },
		"unknown kinds": func(f *atlasv1.Atlas) { f.Connectors[0].Kinds |= 64 },
		"duplicate tile": func(f *atlasv1.Atlas) {
			f.Connectors[1].Map, f.Connectors[1].X, f.Connectors[1].Y = f.Connectors[0].Map, f.Connectors[0].X, f.Connectors[0].Y
		},
		"edge target": func(f *atlasv1.Atlas) { firstEdge(f).To = uint32(len(f.Connectors)) },
		"edge cost":   func(f *atlasv1.Atlas) { firstEdge(f).Cost = 0 },
		"edge kind":   func(f *atlasv1.Atlas) { firstEdge(f).Kind = 9 },
		"exit retargeted": func(f *atlasv1.Atlas) {
			for _, c := range f.Connectors {
				for _, e := range c.Edges {
					if e.Kind == atlasv1.EdgeKind_EDGE_KIND_EXIT {
						e.To = (e.To + 1) % uint32(len(f.Connectors))
						return
					}
				}
			}
		},
		"exit dropped": func(f *atlasv1.Atlas) {
			for _, c := range f.Connectors {
				for i, e := range c.Edges {
					if e.Kind == atlasv1.EdgeKind_EDGE_KIND_EXIT {
						c.Edges = append(c.Edges[:i], c.Edges[i+1:]...)
						return
					}
				}
			}
		},
		"walk cost above bound": func(f *atlasv1.Atlas) {
			for _, c := range f.Connectors {
				for _, e := range c.Edges {
					if e.Kind == atlasv1.EdgeKind_EDGE_KIND_WALK {
						e.Cost = 1 << 30
						return
					}
				}
			}
		},
		"walk cost below bound": func(f *atlasv1.Atlas) {
			for _, c := range f.Connectors {
				for _, e := range c.Edges {
					if e.Kind == atlasv1.EdgeKind_EDGE_KIND_WALK {
						e.Cost = 1
						return
					}
				}
			}
		},
		"walks dropped": func(f *atlasv1.Atlas) {
			for _, c := range f.Connectors {
				var kept []*atlasv1.Edge
				for _, e := range c.Edges {
					if e.Kind != atlasv1.EdgeKind_EDGE_KIND_WALK {
						kept = append(kept, e)
					}
				}
				c.Edges = kept
			}
		},
		"walk repeated": func(f *atlasv1.Atlas) {
			for _, c := range f.Connectors {
				for _, e := range c.Edges {
					if e.Kind == atlasv1.EdgeKind_EDGE_KIND_WALK {
						c.Edges = append(c.Edges, e)
						return
					}
				}
			}
		},
		"extra connector": func(f *atlasv1.Atlas) {
			f.Connectors = append(f.Connectors, f.Connectors[len(f.Connectors)-1])
		},
		"walk across maps": func(f *atlasv1.Atlas) {
			for _, c := range f.Connectors {
				for _, e := range c.Edges {
					if e.Kind == atlasv1.EdgeKind_EDGE_KIND_EXIT {
						e.Kind = atlasv1.EdgeKind_EDGE_KIND_WALK
						return
					}
				}
			}
		},
	} {
		f := atlas.Build(w).Encode()
		mutate(f)
		if _, err := atlas.Decode(w, f); err == nil || errors.Is(err, atlas.ErrMismatch) {
			t.Errorf("%s: %v", name, err)
		}
	}
	path := filepath.Join(t.TempDir(), "junk.atlas")
	if err := os.WriteFile(path, []byte{0xff, 0xff, 0xff}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := atlas.Load(w, path); err == nil {
		t.Fatal("loaded a corrupt file")
	}
	data, _ := proto.Marshal(atlas.Build(w).Encode())
	if len(data) == 0 {
		t.Fatal("empty encoding")
	}
}

func firstEdge(f *atlasv1.Atlas) *atlasv1.Edge {
	for _, c := range f.Connectors {
		if len(c.Edges) > 0 {
			return c.Edges[0]
		}
	}
	panic("no edges")
}

func TestAtlasFilesCannotWalkBetweenComponents(t *testing.T) {
	// A wall splits the hall; each half has a portal to the yard.
	c := fixture.New(t)
	c.Map("/t/hall", ".#.").
		Object("portal", 0, 0, "slaying /t/yard", "hp 0", "sp 0").
		Object("portal", 2, 0, "slaying /t/yard", "hp 1", "sp 0")
	c.Map("/t/yard", "..")
	w, err := content.Load(c.Write())
	if err != nil {
		t.Fatal(err)
	}
	a := atlas.Build(w)
	hall, _ := w.Lookup("/t/hall")
	west, _ := a.Lookup(hall, 0, 0)
	east, _ := a.Lookup(hall, 2, 0)
	f := a.Encode()
	f.Connectors[west].Edges = append(f.Connectors[west].Edges, &atlasv1.Edge{
		To: uint32(east), Cost: 6, Kind: atlasv1.EdgeKind_EDGE_KIND_WALK,
	})
	if _, err := atlas.Decode(w, f); err == nil {
		t.Fatal("accepted a walk through a wall")
	}
}
