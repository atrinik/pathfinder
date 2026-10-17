package server_test

import (
	"context"
	"net"
	"testing"

	pb "github.com/atrinik/pathfinder/gen/go/atrinik/pathfinder/v1"
	"github.com/atrinik/pathfinder/internal/atlas"
	"github.com/atrinik/pathfinder/internal/content"
	"github.com/atrinik/pathfinder/internal/fixture"
	"github.com/atrinik/pathfinder/internal/search"
	"github.com/atrinik/pathfinder/internal/server"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func client(t *testing.T, limits server.Limits) pb.PathfinderServiceClient {
	t.Helper()
	c := fixture.New(t)
	c.Map("/w/world_0_0", "...", ".#.", "...").
		Object("portal", 0, 0, "slaying /w/inn", "hp 1", "sp 1")
	c.Map("/w/world_1_0", "...", "...", "...")
	c.Map("/w/inn", "..", "..")
	w, err := content.Load(c.Write())
	if err != nil {
		t.Fatal(err)
	}
	listener := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	pb.RegisterPathfinderServiceServer(srv, server.New(search.NewEngine(w, 4), atlas.NewPlanner(atlas.Build(w), 4), limits))
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return pb.NewPathfinderServiceClient(conn)
}

func pos(m string, x, y int32) *pb.Position { return &pb.Position{Map: m, X: x, Y: y} }

func TestFindPathOverGRPC(t *testing.T) {
	api := client(t, server.DefaultLimits)
	ctx := context.Background()
	resp, err := api.FindPath(ctx, &pb.FindPathRequest{
		Start: pos("/w/world_0_0", 0, 1),
		Goal:  &pb.FindPathRequest_GoalTile{GoalTile: pos("/w/world_1_0", 1, 1)},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The wall at (1,1) rules out both diagonal shortcuts under strict
	// corners: up, along the top row, across the seam, then down.
	if resp.GetStatus() != pb.PathStatus_PATH_STATUS_FOUND || resp.GetCost() != 5 {
		t.Fatalf("status %v cost %d", resp.GetStatus(), resp.GetCost())
	}
	steps := resp.GetSteps()
	if steps[0].GetKind() != pb.StepKind_STEP_KIND_START || steps[len(steps)-1].GetPosition().GetMap() != "/w/world_1_0" {
		t.Fatalf("steps %v", steps)
	}
	seam := false
	for _, step := range steps {
		if step.GetKind() == pb.StepKind_STEP_KIND_SEAM {
			seam = true
		}
	}
	if !seam || resp.GetMetrics().GetExpanded() == 0 {
		t.Fatalf("missing seam step or metrics: %v", resp)
	}

	resp, err = api.FindPath(ctx, &pb.FindPathRequest{
		Start: pos("/w/world_0_0", 2, 2),
		Goal:  &pb.FindPathRequest_GoalMap{GoalMap: "/w/inn"},
	})
	if err != nil || resp.GetStatus() != pb.PathStatus_PATH_STATUS_FOUND {
		t.Fatalf("goal map: %v %v", resp, err)
	}
	if last := resp.GetSteps()[len(resp.GetSteps())-1]; last.GetKind() != pb.StepKind_STEP_KIND_EXIT || last.GetAutomatic() {
		t.Fatalf("portal step %v", last)
	}

	resp, err = api.FindPath(ctx, &pb.FindPathRequest{
		Start: pos("/w/world_0_0", 0, 1),
		Goal:  &pb.FindPathRequest_GoalTile{GoalTile: pos("/w/world_1_0", 1, 1)},
		Agent: &pb.AgentProfile{StayOnMap: true},
	})
	if err != nil || resp.GetStatus() != pb.PathStatus_PATH_STATUS_NO_PATH {
		t.Fatalf("stay on map: %v %v", resp, err)
	}
}

func TestFindPathRejectsMalformedRequests(t *testing.T) {
	limits := server.DefaultLimits
	limits.MaxBlocked = 1
	api := client(t, limits)
	ctx := context.Background()
	for name, tc := range map[string]struct {
		req  *pb.FindPathRequest
		code codes.Code
	}{
		"missing start": {&pb.FindPathRequest{Goal: &pb.FindPathRequest_GoalMap{GoalMap: "/w/inn"}}, codes.InvalidArgument},
		"unknown map": {&pb.FindPathRequest{Start: pos("/nowhere", 0, 0),
			Goal: &pb.FindPathRequest_GoalMap{GoalMap: "/w/inn"}}, codes.NotFound},
		"off the map": {&pb.FindPathRequest{Start: pos("/w/inn", 5, 0),
			Goal: &pb.FindPathRequest_GoalMap{GoalMap: "/w/inn"}}, codes.OutOfRange},
		"missing goal": {&pb.FindPathRequest{Start: pos("/w/inn", 0, 0)}, codes.InvalidArgument},
		"too many blocked": {&pb.FindPathRequest{Start: pos("/w/inn", 0, 0),
			Goal:    &pb.FindPathRequest_GoalMap{GoalMap: "/w/inn"},
			Blocked: []*pb.Position{pos("/w/inn", 1, 0), pos("/w/inn", 1, 1)}}, codes.InvalidArgument},
		"unknown corner policy": {&pb.FindPathRequest{Start: pos("/w/inn", 0, 0),
			Goal:  &pb.FindPathRequest_GoalMap{GoalMap: "/w/inn"},
			Agent: &pb.AgentProfile{Corners: 99}}, codes.InvalidArgument},
	} {
		_, err := api.FindPath(ctx, tc.req)
		if status.Code(err) != tc.code {
			t.Errorf("%s: got %v, want %v", name, err, tc.code)
		}
	}
}

func TestFindPathStopsForACanceledRequest(t *testing.T) {
	c := fixture.New(t)
	c.Map("/w/inn", "..", "..")
	w, err := content.Load(c.Write())
	if err != nil {
		t.Fatal(err)
	}
	svc := server.New(search.NewEngine(w, 1), atlas.NewPlanner(atlas.Build(w), 1), server.DefaultLimits)
	req := &pb.FindPathRequest{Start: pos("/w/inn", 0, 0), Goal: &pb.FindPathRequest_GoalTile{GoalTile: pos("/w/inn", 1, 1)}}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.FindPath(canceled, req); status.Code(err) != codes.Canceled {
		t.Errorf("canceled: %v", err)
	}
	if _, err := svc.FindPath(context.Background(), req); err != nil {
		t.Errorf("live request: %v", err)
	}
}

func TestDescribeMapAndWorldInfo(t *testing.T) {
	api := client(t, server.DefaultLimits)
	ctx := context.Background()
	m, err := api.DescribeMap(ctx, &pb.DescribeMapRequest{Map: "/w/world_0_0"})
	if err != nil {
		t.Fatal(err)
	}
	if m.GetWidth() != 3 || m.GetWalkableTiles() != 8 {
		t.Fatalf("map %v", m)
	}
	var seams, exits int
	for _, tr := range m.GetTransitions() {
		switch tr.GetKind() {
		case pb.TransitionKind_TRANSITION_KIND_SEAM:
			seams++
			if tr.GetTo().GetMap() != "/w/world_1_0" || tr.GetFrom().GetX() != 3 {
				t.Errorf("seam %v", tr)
			}
		case pb.TransitionKind_TRANSITION_KIND_EXIT:
			exits++
			if tr.GetTo().GetMap() != "/w/inn" || tr.GetTo().GetX() != 1 {
				t.Errorf("exit %v", tr)
			}
		}
	}
	if seams != 1 || exits != 1 {
		t.Fatalf("seams %d exits %d", seams, exits)
	}
	if _, err := api.DescribeMap(ctx, &pb.DescribeMapRequest{Map: "/nowhere"}); status.Code(err) != codes.NotFound {
		t.Fatalf("unknown map: %v", err)
	}
	info, err := api.GetWorldInfo(ctx, &pb.GetWorldInfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if info.GetMaps() != 3 || info.GetTiles() != 22 || info.GetTransitions() != 3 || len(info.GetContentDigest()) != 64 {
		t.Fatalf("info %v", info)
	}
}
