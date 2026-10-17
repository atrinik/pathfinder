// Command atlas builds the offline atlas of a content checkout: the
// connector graph pathfinderd plans over, written once so the daemon can
// load it with -atlas instead of building it at startup.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/atrinik/pathfinder/internal/atlas"
	"github.com/atrinik/pathfinder/internal/content"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "atlas:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("atlas", flag.ContinueOnError)
	contentRoot := flags.String("content", "", "content checkout holding arch/ and maps/ (required)")
	out := flags.String("out", "", "atlas file to write (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *contentRoot == "" || *out == "" {
		return errors.New("-content and -out are required")
	}
	started := time.Now()
	w, err := content.Load(*contentRoot)
	if err != nil {
		return fmt.Errorf("load content: %w", err)
	}
	a := atlas.Build(w)
	if err := a.Save(*out); err != nil {
		return err
	}
	info, err := os.Stat(*out)
	if err != nil {
		return err
	}
	s := a.Stats()
	fmt.Printf("%s: %d maps, %d connectors, %d edges, %d road tiles; %d bytes, digest %s (build %s, total %s)\n",
		*out, len(w.Maps), s.Connectors, s.Edges, s.RoadTiles, info.Size(), w.Digest,
		a.BuildTime.Round(time.Millisecond), time.Since(started).Round(time.Millisecond))
	return nil
}
