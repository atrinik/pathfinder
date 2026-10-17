// Command pathfinderd serves the PathfinderService gRPC API over one content
// checkout.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	pb "github.com/atrinik/pathfinder/gen/go/atrinik/pathfinder/v1"
	"github.com/atrinik/pathfinder/internal/content"
	"github.com/atrinik/pathfinder/internal/search"
	"github.com/atrinik/pathfinder/internal/server"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "pathfinderd:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("pathfinderd", flag.ContinueOnError)
	contentRoot := flags.String("content", "", "content checkout holding arch/ and maps/ (required)")
	listen := flags.String("listen", "127.0.0.1:7843", "TCP address to serve gRPC on")
	maxExpanded := flags.Int("max-expanded", server.DefaultLimits.MaxExpanded, "upper bound on states one path search may expand")
	maxConcurrent := flags.Int("max-concurrent", runtime.GOMAXPROCS(0),
		"searches run at once; each holds about 17 bytes per world tile, and more are rejected as RESOURCE_EXHAUSTED")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *contentRoot == "" {
		return errors.New("-content is required")
	}
	if *maxConcurrent < 1 {
		return errors.New("-max-concurrent must be at least 1")
	}
	if *maxExpanded < 1 {
		return errors.New("-max-expanded must be at least 1")
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	started := time.Now()
	w, err := content.Load(*contentRoot)
	if err != nil {
		return fmt.Errorf("load content: %w", err)
	}
	logger.Info("content loaded", "maps", len(w.Maps), "tiles", w.States,
		"transitions", w.Transitions(), "digest", w.Digest, "duration", time.Since(started))

	limits := server.DefaultLimits
	limits.MaxExpanded = *maxExpanded
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	grpcServer := grpc.NewServer()
	pb.RegisterPathfinderServiceServer(grpcServer, server.New(search.NewEngine(w, *maxConcurrent), limits))
	healthServer := health.NewServer()
	healthServer.SetServingStatus(pb.PathfinderService_ServiceDesc.ServiceName, healthpb.HealthCheckResponse_SERVING)
	healthServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(grpcServer, healthServer)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		healthServer.Shutdown()
		grpcServer.GracefulStop()
	}()
	logger.Info("serving", "address", listener.Addr().String())
	return grpcServer.Serve(listener)
}
