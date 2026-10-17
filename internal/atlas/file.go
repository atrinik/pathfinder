package atlas

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
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
	Stamp(f)
	return f
}

// Stamp sets an atlas file message's graph digest from its connectors.
func Stamp(f *atlasv1.Atlas) {
	f.GraphDigest = graphDigest(f)
}

// graphDigest hashes the connector graph in a canonical byte encoding.
func graphDigest(f *atlasv1.Atlas) []byte {
	h := sha256.New()
	var buf [4]byte
	put := func(v uint32) {
		binary.BigEndian.PutUint32(buf[:], v)
		h.Write(buf[:])
	}
	put(uint32(len(f.GetConnectors())))
	for _, c := range f.GetConnectors() {
		put(c.GetMap())
		put(uint32(c.GetX()))
		put(uint32(c.GetY()))
		put(c.GetKinds())
		put(uint32(len(c.GetEdges())))
		for _, e := range c.GetEdges() {
			put(e.GetTo())
			put(e.GetCost())
			put(uint32(e.GetKind()))
			if e.GetAutomatic() {
				put(1)
			} else {
				put(0)
			}
			put(uint32(len(e.GetLabel())))
			h.Write([]byte(e.GetLabel()))
		}
	}
	return h.Sum(nil)
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
	// The connectors and their exits and seams follow cheaply from the
	// content, so they are recomputed and the file must agree with them
	// exactly. Only the walks, the costly part, come from the file; each must
	// join two connectors walkable from one component, so every walk a plan
	// re-derives exists. A corrupted walk cost can make a plan suboptimal but
	// never impossible.
	a := newAtlas(w)
	a.addConnectors()
	a.Out = make([][]Edge, len(a.Connectors))
	a.addTransitions()
	a.groupConnectors()
	n := len(a.Connectors)
	if len(f.GetConnectors()) != n {
		return nil, fmt.Errorf("%d connectors, the content has %d", len(f.GetConnectors()), n)
	}
	for id, pc := range f.GetConnectors() {
		c := a.Connectors[id]
		if pc.GetMap() != uint32(c.Map) || pc.GetX() != c.X || pc.GetY() != c.Y || pc.GetKinds() != uint32(c.Kind) {
			return nil, fmt.Errorf("connector %d differs from the content's (%s %d,%d kinds %#x)",
				id, w.Maps[c.Map].Path, c.X, c.Y, uint32(c.Kind))
		}
		transitions := a.Out[id]
		matched := 0
		// Exactly one walk to every other connector walkable from this
		// connector's component.
		required := map[int32]bool{}
		if c.Component >= 0 {
			for _, to := range a.componentConnectors[c.Map][c.Component] {
				if to != int32(id) {
					required[to] = true
				}
			}
		}
		var walks []Edge
		for _, pe := range pc.GetEdges() {
			switch pe.GetKind() {
			case atlasv1.EdgeKind_EDGE_KIND_WALK:
				e, err := a.walkEdge(int32(id), pe)
				if err != nil {
					return nil, fmt.Errorf("connector %d: %w", id, err)
				}
				if !required[e.To] {
					return nil, fmt.Errorf("connector %d: unexpected or repeated walk to connector %d", id, e.To)
				}
				delete(required, e.To)
				walks = append(walks, e)
			case atlasv1.EdgeKind_EDGE_KIND_EXIT, atlasv1.EdgeKind_EDGE_KIND_SEAM:
				if matched == len(transitions) || !sameTransition(transitions[matched], pe) {
					return nil, fmt.Errorf("connector %d: an exit or seam edge differs from the content's", id)
				}
				matched++
			default:
				return nil, fmt.Errorf("connector %d: unknown edge kind %d", id, pe.GetKind())
			}
		}
		if matched != len(transitions) {
			return nil, fmt.Errorf("connector %d: %d of its %d exits and seams are missing", id, len(transitions)-matched, len(transitions))
		}
		if len(required) > 0 {
			return nil, fmt.Errorf("connector %d: %d walks are missing", id, len(required))
		}
		a.Out[id] = append(transitions, walks...)
	}
	// Last, so the checks above report what is structurally wrong: walk
	// costs can only be checked against their bounds, and the digest catches
	// any other damage to them.
	if !bytes.Equal(f.GetGraphDigest(), graphDigest(f)) {
		return nil, errors.New("connector graph digest does not match its contents")
	}
	a.stats = a.count()
	a.Loaded = true
	return a, nil
}

// walkEdge checks one walk edge read from a file.
func (a *Atlas) walkEdge(from int32, pe *atlasv1.Edge) (Edge, error) {
	if int(pe.GetTo()) >= len(a.Connectors) || int32(pe.GetTo()) == from {
		return Edge{}, fmt.Errorf("walk to connector %d", pe.GetTo())
	}
	fc, tc := a.Connectors[from], a.Connectors[pe.GetTo()]
	// A walk costs at least a road step per tile of distance, and at most a
	// plain step per tile of its map (plus the step onto an automatic exit),
	// which also keeps route totals far from overflowing.
	floor := RoadStep * max(abs32(fc.X-tc.X), abs32(fc.Y-tc.Y))
	ceiling := PlainStep * (len(a.World.Maps[fc.Map].Terrain) + 1)
	if pe.GetCost() < uint32(floor) || int64(pe.GetCost()) > int64(ceiling) || pe.GetAutomatic() || pe.GetLabel() != "" {
		return Edge{}, fmt.Errorf("walk to connector %d: bad cost %d or attributes", pe.GetTo(), pe.GetCost())
	}
	return Edge{To: int32(pe.GetTo()), Cost: int32(pe.GetCost()), Kind: WalkEdge}, nil
}

func sameTransition(e Edge, pe *atlasv1.Edge) bool {
	return pe.GetKind() == edgeKinds[e.Kind] && int32(pe.GetTo()) == e.To && int32(pe.GetCost()) == e.Cost &&
		pe.GetAutomatic() == e.Automatic && pe.GetLabel() == e.Label
}

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}
