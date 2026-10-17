// Package server implements the PathfinderService gRPC API.
package server

import (
	"context"
	"fmt"
	"sort"

	pb "github.com/atrinik/pathfinder/gen/go/atrinik/pathfinder/v1"
	"github.com/atrinik/pathfinder/internal/search"
	"github.com/atrinik/pathfinder/internal/world"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Limits bound what one request may ask for.
type Limits struct {
	// MaxExpanded caps (and defaults) a query's expanded states.
	MaxExpanded int
	// MaxBlocked caps the temporary blocked tiles of one query.
	MaxBlocked int
	// MaxListed caps the keys or avoided maps of one agent.
	MaxListed int
}

// DefaultLimits are the service's limits when none are configured.
var DefaultLimits = Limits{MaxExpanded: search.DefaultMaxExpanded, MaxBlocked: 4096, MaxListed: 1024}

// Service serves one loaded world.
type Service struct {
	pb.UnimplementedPathfinderServiceServer
	engine *search.Engine
	limits Limits
}

// New returns a service over engine.
func New(engine *search.Engine, limits Limits) *Service {
	return &Service{engine: engine, limits: limits}
}

func (s *Service) mapOf(path, field string) (*world.Map, error) {
	m, ok := s.engine.World().Lookup(path)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "%s: unknown map %q", field, path)
	}
	return m, nil
}

func (s *Service) position(p *pb.Position, field string) (search.Position, error) {
	if p == nil {
		return search.Position{}, status.Errorf(codes.InvalidArgument, "%s is required", field)
	}
	m, err := s.mapOf(p.GetMap(), field)
	if err != nil {
		return search.Position{}, err
	}
	if !m.InBounds(p.GetX(), p.GetY()) {
		return search.Position{}, status.Errorf(codes.OutOfRange,
			"%s (%d, %d) is outside %s (%dx%d)", field, p.GetX(), p.GetY(), m.Path, m.Width, m.Height)
	}
	return search.Position{Map: m, X: p.GetX(), Y: p.GetY()}, nil
}

func (s *Service) agent(p *pb.AgentProfile) (search.Agent, error) {
	agent := search.Agent{TerrainMask: p.GetTerrainMask(), StayOnMap: p.GetStayOnMap()}
	if len(p.GetKeys()) > s.limits.MaxListed || len(p.GetAvoidedMaps()) > s.limits.MaxListed {
		return agent, status.Errorf(codes.InvalidArgument, "at most %d keys and avoided maps", s.limits.MaxListed)
	}
	switch p.GetCorners() {
	case pb.CornerPolicy_CORNER_POLICY_UNSPECIFIED, pb.CornerPolicy_CORNER_POLICY_STRICT:
		agent.Corners = search.CornersStrict
	case pb.CornerPolicy_CORNER_POLICY_LOOSE:
		agent.Corners = search.CornersLoose
	case pb.CornerPolicy_CORNER_POLICY_NO_DIAGONALS:
		agent.Corners = search.NoDiagonals
	default:
		return agent, status.Errorf(codes.InvalidArgument, "unknown corner policy %d", p.GetCorners())
	}
	if len(p.GetKeys()) > 0 {
		agent.Keys = make(map[string]bool, len(p.GetKeys()))
		for _, key := range p.GetKeys() {
			agent.Keys[key] = true
		}
	}
	if len(p.GetAvoidedMaps()) > 0 {
		agent.Avoided = make(map[int32]bool, len(p.GetAvoidedMaps()))
		for _, path := range p.GetAvoidedMaps() {
			// Unknown avoided maps cannot be entered anyway.
			if m, ok := s.engine.World().Lookup(path); ok {
				agent.Avoided[m.Index] = true
			}
		}
	}
	return agent, nil
}

// FindPath implements PathfinderService.FindPath.
func (s *Service) FindPath(ctx context.Context, req *pb.FindPathRequest) (*pb.FindPathResponse, error) {
	start, err := s.position(req.GetStart(), "start")
	if err != nil {
		return nil, err
	}
	q := search.Query{Start: start}
	switch goal := req.GetGoal().(type) {
	case *pb.FindPathRequest_GoalTile:
		tile, err := s.position(goal.GoalTile, "goal_tile")
		if err != nil {
			return nil, err
		}
		q.GoalTile = &tile
	case *pb.FindPathRequest_GoalMap:
		m, err := s.mapOf(goal.GoalMap, "goal_map")
		if err != nil {
			return nil, err
		}
		q.GoalMap = m
	default:
		return nil, status.Error(codes.InvalidArgument, "goal_tile or goal_map is required")
	}
	if q.Agent, err = s.agent(req.GetAgent()); err != nil {
		return nil, err
	}
	if len(req.GetBlocked()) > s.limits.MaxBlocked {
		return nil, status.Errorf(codes.InvalidArgument, "at most %d blocked tiles", s.limits.MaxBlocked)
	}
	if len(req.GetBlocked()) > 0 {
		q.Blocked = make(map[int32]bool, len(req.GetBlocked()))
		for i, p := range req.GetBlocked() {
			tile, err := s.position(p, fmt.Sprintf("blocked[%d]", i))
			if err != nil {
				return nil, err
			}
			q.Blocked[s.engine.World().State(tile.Map, tile.X, tile.Y)] = true
		}
	}
	q.MaxExpanded = s.limits.MaxExpanded
	if requested := int(req.GetMaxExpanded()); requested > 0 && requested < q.MaxExpanded {
		q.MaxExpanded = requested
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	result, err := s.engine.FindPath(q)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	return toResponse(result), nil
}

func toResponse(r search.Result) *pb.FindPathResponse {
	resp := &pb.FindPathResponse{
		Cost: uint32(r.Cost),
		Metrics: &pb.SearchMetrics{
			Expanded:       uint64(r.Expanded),
			Generated:      uint64(r.Generated),
			DurationMicros: uint64(r.Duration.Microseconds()),
		},
	}
	switch r.Status {
	case search.Found:
		resp.Status = pb.PathStatus_PATH_STATUS_FOUND
	case search.LimitReached:
		resp.Status = pb.PathStatus_PATH_STATUS_LIMIT_REACHED
	default:
		resp.Status = pb.PathStatus_PATH_STATUS_NO_PATH
	}
	resp.Steps = make([]*pb.Step, len(r.Steps))
	for i, step := range r.Steps {
		resp.Steps[i] = &pb.Step{
			Position:  &pb.Position{Map: step.Map.Path, X: step.X, Y: step.Y},
			Kind:      stepKind(step.Kind),
			Automatic: step.Automatic,
		}
	}
	return resp
}

func stepKind(k search.Kind) pb.StepKind {
	switch k {
	case search.Start:
		return pb.StepKind_STEP_KIND_START
	case search.Walk:
		return pb.StepKind_STEP_KIND_WALK
	case search.Seam:
		return pb.StepKind_STEP_KIND_SEAM
	case search.ExitUsed:
		return pb.StepKind_STEP_KIND_EXIT
	}
	return pb.StepKind_STEP_KIND_UNSPECIFIED
}

// DescribeMap implements PathfinderService.DescribeMap.
func (s *Service) DescribeMap(_ context.Context, req *pb.DescribeMapRequest) (*pb.DescribeMapResponse, error) {
	m, err := s.mapOf(req.GetMap(), "map")
	if err != nil {
		return nil, err
	}
	w := s.engine.World()
	resp := &pb.DescribeMapResponse{
		Map: m.Path, Name: m.Name, Region: m.Region, Width: m.Width, Height: m.Height,
	}
	for tile, terrain := range m.Terrain {
		if terrain&world.TerrainWalk != 0 && !m.Blocked[tile] {
			resp.WalkableTiles++
		}
	}
	for dir, seam := range m.Seams {
		if seam < 0 {
			continue
		}
		bx, by := borderPoint(m, world.Deltas[dir])
		resp.Transitions = append(resp.Transitions, &pb.Transition{
			Kind: pb.TransitionKind_TRANSITION_KIND_SEAM,
			From: &pb.Position{Map: m.Path, X: bx, Y: by},
			To:   &pb.Position{Map: w.Maps[seam].Path},
		})
	}
	tiles := make([]int32, 0, len(m.Exits))
	for tile := range m.Exits {
		tiles = append(tiles, tile)
	}
	sort.Slice(tiles, func(i, j int) bool { return tiles[i] < tiles[j] })
	for _, tile := range tiles {
		for _, exit := range m.Exits[tile] {
			resp.Transitions = append(resp.Transitions, &pb.Transition{
				Kind:      pb.TransitionKind_TRANSITION_KIND_EXIT,
				From:      &pb.Position{Map: m.Path, X: tile % m.Width, Y: tile / m.Width},
				To:        &pb.Position{Map: w.Maps[exit.To].Path, X: exit.X, Y: exit.Y},
				Automatic: exit.Automatic,
				Label:     exit.Label,
			})
		}
	}
	return resp, nil
}

// borderPoint is a representative square just outside m in direction d.
func borderPoint(m *world.Map, d [2]int32) (int32, int32) {
	pick := func(delta, size int32) int32 {
		switch delta {
		case -1:
			return -1
		case 1:
			return size
		}
		return size / 2
	}
	return pick(d[0], m.Width), pick(d[1], m.Height)
}

// GetWorldInfo implements PathfinderService.GetWorldInfo.
func (s *Service) GetWorldInfo(context.Context, *pb.GetWorldInfoRequest) (*pb.GetWorldInfoResponse, error) {
	w := s.engine.World()
	return &pb.GetWorldInfoResponse{
		Maps:          uint32(len(w.Maps)),
		Tiles:         uint64(w.States),
		Transitions:   uint32(w.Transitions()),
		ContentDigest: w.Digest,
	}, nil
}
