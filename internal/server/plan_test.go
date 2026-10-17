package server_test

import (
	"context"
	"testing"

	pb "github.com/atrinik/pathfinder/gen/go/atrinik/pathfinder/v1"
	"github.com/atrinik/pathfinder/internal/server"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestPlanOverGRPC(t *testing.T) {
	api := client(t, server.DefaultLimits)
	ctx := context.Background()
	resp, err := api.Plan(ctx, &pb.PlanRequest{
		Start: pos("/w/world_1_0", 2, 2),
		Goal:  &pb.PlanRequest_GoalMap{GoalMap: "/w/inn"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var kinds []pb.WaypointKind
	for _, wp := range resp.GetWaypoints() {
		kinds = append(kinds, wp.GetKind())
	}
	// Across the seam, round the wall at (1,1) to the portal in the corner,
	// through it.
	want := []pb.WaypointKind{
		pb.WaypointKind_WAYPOINT_KIND_START, pb.WaypointKind_WAYPOINT_KIND_SEAM,
		pb.WaypointKind_WAYPOINT_KIND_ARRIVE, pb.WaypointKind_WAYPOINT_KIND_WALK,
		pb.WaypointKind_WAYPOINT_KIND_EXIT, pb.WaypointKind_WAYPOINT_KIND_GOAL,
	}
	if resp.GetStatus() != pb.PathStatus_PATH_STATUS_FOUND || len(kinds) != len(want) {
		t.Fatalf("status %v waypoints %v", resp.GetStatus(), resp.GetWaypoints())
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("waypoints %v", resp.GetWaypoints())
		}
	}
	exit := resp.GetWaypoints()[4]
	if exit.GetPosition().GetMap() != "/w/world_0_0" || exit.GetPosition().GetX() != 0 || exit.GetLabel() != "portal" || exit.GetAutomatic() {
		t.Fatalf("exit %v", exit)
	}
	if goal := resp.GetWaypoints()[5].GetPosition(); goal.GetMap() != "/w/inn" || goal.GetX() != 1 || goal.GetY() != 1 {
		t.Fatalf("goal %v", goal)
	}
	if resp.GetTiles() == 0 || resp.GetCost() < 2*resp.GetTiles() || resp.GetMetrics().GetExpanded() == 0 {
		t.Fatalf("totals %v", resp)
	}

	// Staying off world_0_0 leaves no way into the inn.
	resp, err = api.Plan(ctx, &pb.PlanRequest{
		Start:       pos("/w/world_1_0", 2, 2),
		Goal:        &pb.PlanRequest_GoalMap{GoalMap: "/w/inn"},
		AvoidedMaps: []string{"/w/world_0_0", "/nowhere"},
	})
	if err != nil || resp.GetStatus() != pb.PathStatus_PATH_STATUS_NO_PATH || len(resp.GetWaypoints()) != 0 {
		t.Fatalf("avoided: %v %v", resp, err)
	}

	// A tight expansion bound stops the plan short.
	resp, err = api.Plan(ctx, &pb.PlanRequest{
		Start:       pos("/w/world_1_0", 2, 2),
		Goal:        &pb.PlanRequest_GoalMap{GoalMap: "/w/inn"},
		MaxExpanded: 1,
	})
	if err != nil || resp.GetStatus() != pb.PathStatus_PATH_STATUS_LIMIT_REACHED || len(resp.GetWaypoints()) != 0 {
		t.Fatalf("limited: %v %v", resp, err)
	}

	// A huge spacing keeps only the turns.
	resp, err = api.Plan(ctx, &pb.PlanRequest{
		Start:   pos("/w/world_0_0", 0, 1),
		Goal:    &pb.PlanRequest_GoalTile{GoalTile: pos("/w/world_0_0", 2, 1)},
		Spacing: 1 << 31,
	})
	if err != nil || resp.GetStatus() != pb.PathStatus_PATH_STATUS_FOUND {
		t.Fatalf("spacing: %v %v", resp, err)
	}
}

func TestPlanRejectsMalformedRequests(t *testing.T) {
	limits := server.DefaultLimits
	limits.MaxListed = 1
	api := client(t, limits)
	ctx := context.Background()
	for name, tc := range map[string]struct {
		req  *pb.PlanRequest
		code codes.Code
	}{
		"missing start": {&pb.PlanRequest{Goal: &pb.PlanRequest_GoalMap{GoalMap: "/w/inn"}}, codes.InvalidArgument},
		"unknown start map": {&pb.PlanRequest{Start: pos("/nowhere", 0, 0),
			Goal: &pb.PlanRequest_GoalMap{GoalMap: "/w/inn"}}, codes.NotFound},
		"unknown goal map": {&pb.PlanRequest{Start: pos("/w/inn", 0, 0),
			Goal: &pb.PlanRequest_GoalMap{GoalMap: "/nowhere"}}, codes.NotFound},
		"goal off the map": {&pb.PlanRequest{Start: pos("/w/inn", 0, 0),
			Goal: &pb.PlanRequest_GoalTile{GoalTile: pos("/w/inn", 9, 9)}}, codes.OutOfRange},
		"missing goal": {&pb.PlanRequest{Start: pos("/w/inn", 0, 0)}, codes.InvalidArgument},
		"too many avoided": {&pb.PlanRequest{Start: pos("/w/inn", 0, 0),
			Goal:        &pb.PlanRequest_GoalMap{GoalMap: "/w/inn"},
			AvoidedMaps: []string{"/w/world_0_0", "/w/world_1_0"}}, codes.InvalidArgument},
	} {
		_, err := api.Plan(ctx, tc.req)
		if status.Code(err) != tc.code {
			t.Errorf("%s: got %v, want %v", name, err, tc.code)
		}
	}
}

func TestGetAtlasInfo(t *testing.T) {
	api := client(t, server.DefaultLimits)
	ctx := context.Background()
	info, err := api.GetAtlasInfo(ctx, &pb.GetAtlasInfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	world, err := api.GetWorldInfo(ctx, &pb.GetWorldInfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if info.GetConnectors() == 0 || info.GetEdges() == 0 || info.GetSource() != pb.AtlasSource_ATLAS_SOURCE_BUILT ||
		info.GetContentDigest() != world.GetContentDigest() {
		t.Fatalf("atlas info %v", info)
	}
}
