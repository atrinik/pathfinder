package atlas

import (
	"errors"
	"fmt"
	"os"
	"time"

	atlasv1 "github.com/atrinik/pathfinder/gen/go/atrinik/pathfinder/atlas/v1"
	"github.com/atrinik/pathfinder/internal/world"
	"google.golang.org/protobuf/proto"
)

// FileFormat is the atlas file format version this package reads and writes.
const FileFormat = 1

// ErrMismatch reports an atlas file built from other content, or in another
// format, than the world it is loaded against.
var ErrMismatch = errors.New("atlas file does not match the loaded content")

const allKinds = AppliedExit | AutomaticExit | Arrival | Crossing

var edgeKinds = map[EdgeKind]atlasv1.EdgeKind{
	WalkEdge: atlasv1.EdgeKind_EDGE_KIND_WALK,
	ExitEdge: atlasv1.EdgeKind_EDGE_KIND_EXIT,
	SeamEdge: atlasv1.EdgeKind_EDGE_KIND_SEAM,
}

// Encode returns the atlas as an atlas file message.
func (a *Atlas) Encode() *atlasv1.Atlas {
	f := &atlasv1.Atlas{
		Format:        FileFormat,
		ContentDigest: a.World.Digest,
		Maps:          make([]string, len(a.World.Maps)),
		Connectors:    make([]*atlasv1.Connector, len(a.Connectors)),
	}
	for i, m := range a.World.Maps {
		f.Maps[i] = m.Path
	}
	for id, c := range a.Connectors {
		pc := &atlasv1.Connector{Map: uint32(c.Map), X: c.X, Y: c.Y, Kinds: uint32(c.Kind)}
		for _, e := range a.Out[id] {
			pc.Edges = append(pc.Edges, &atlasv1.Edge{
				To: uint32(e.To), Cost: uint32(e.Cost), Kind: edgeKinds[e.Kind], Automatic: e.Automatic, Label: e.Label,
			})
		}
		f.Connectors[id] = pc
	}
	return f
}

// Save writes the atlas file to path.
func (a *Atlas) Save(path string) error {
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(a.Encode())
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// Load reads an atlas file for w. It fails with ErrMismatch unless the file
// was built from exactly w's content, and rejects any malformed graph.
func Load(w *world.World, path string) (*Atlas, error) {
	started := time.Now()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f atlasv1.Atlas
	if err := proto.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	a, err := Decode(w, &f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	a.BuildTime = time.Since(started)
	return a, nil
}

// Decode rebuilds an atlas for w from an atlas file message.
func Decode(w *world.World, f *atlasv1.Atlas) (*Atlas, error) {
	if f.GetFormat() != FileFormat {
		return nil, fmt.Errorf("%w: format %d, want %d", ErrMismatch, f.GetFormat(), FileFormat)
	}
	if f.GetContentDigest() != w.Digest {
		return nil, fmt.Errorf("%w: content digest %s, loaded %s", ErrMismatch, f.GetContentDigest(), w.Digest)
	}
	if len(f.GetMaps()) != len(w.Maps) {
		return nil, fmt.Errorf("%w: %d maps, loaded %d", ErrMismatch, len(f.GetMaps()), len(w.Maps))
	}
	for i, path := range f.GetMaps() {
		if path != w.Maps[i].Path {
			return nil, fmt.Errorf("%w: map %d is %s, loaded %s", ErrMismatch, i, path, w.Maps[i].Path)
		}
	}
	a := newAtlas(w)
	n := len(f.GetConnectors())
	a.Connectors = make([]Connector, n)
	a.Out = make([][]Edge, n)
	for id, pc := range f.GetConnectors() {
		if int(pc.GetMap()) >= len(w.Maps) {
			return nil, fmt.Errorf("connector %d: map %d out of range", id, pc.GetMap())
		}
		m := w.Maps[pc.GetMap()]
		if !m.InBounds(pc.GetX(), pc.GetY()) {
			return nil, fmt.Errorf("connector %d: (%d, %d) is outside %s", id, pc.GetX(), pc.GetY(), m.Path)
		}
		kind := ConnectorKind(pc.GetKinds())
		if kind == 0 || uint32(kind) != pc.GetKinds() || kind&^allKinds != 0 {
			return nil, fmt.Errorf("connector %d: unknown kinds %#x", id, pc.GetKinds())
		}
		state := w.State(m, pc.GetX(), pc.GetY())
		if _, dup := a.byState[state]; dup {
			return nil, fmt.Errorf("connector %d: duplicate tile %s (%d, %d)", id, m.Path, pc.GetX(), pc.GetY())
		}
		a.byState[state] = int32(id)
		a.Connectors[id] = Connector{
			Map: m.Index, X: pc.GetX(), Y: pc.GetY(), Kind: kind,
			Component: a.Components[m.Index][m.Tile(pc.GetX(), pc.GetY())],
		}
	}
	for id, pc := range f.GetConnectors() {
		for _, pe := range pc.GetEdges() {
			if int(pe.GetTo()) >= n || pe.GetCost() == 0 || pe.GetCost() > 1<<30 {
				return nil, fmt.Errorf("connector %d: bad edge to %d cost %d", id, pe.GetTo(), pe.GetCost())
			}
			e := Edge{To: int32(pe.GetTo()), Cost: int32(pe.GetCost()), Automatic: pe.GetAutomatic(), Label: pe.GetLabel()}
			switch pe.GetKind() {
			case atlasv1.EdgeKind_EDGE_KIND_WALK:
				e.Kind = WalkEdge
				if a.Connectors[id].Map != a.Connectors[e.To].Map {
					return nil, fmt.Errorf("connector %d: walk edge to another map", id)
				}
			case atlasv1.EdgeKind_EDGE_KIND_EXIT:
				e.Kind = ExitEdge
			case atlasv1.EdgeKind_EDGE_KIND_SEAM:
				e.Kind = SeamEdge
			default:
				return nil, fmt.Errorf("connector %d: unknown edge kind %d", id, pe.GetKind())
			}
			a.Out[id] = append(a.Out[id], e)
		}
	}
	a.groupConnectors()
	a.Loaded = true
	return a, nil
}
