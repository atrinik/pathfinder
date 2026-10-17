# Atrinik pathfinder

`pathfinder` is an MIT-licensed, clean-room pathfinding and navigation service
for Atrinik. It loads an authored content checkout and answers movement
queries over gRPC, so any program can share one implementation: the Classic
server, the playtester bot, or the next-generation server. Talking to it over
the network keeps its MIT code separate from GPL clients that consult it.

The service is being built in stages:

1. **Precise pathfinding** (this release): exact tile-by-tile paths for one
   agent, across tiled-map seams, exits and stairs, honouring terrain,
   static blockers, locked doors, diagonal-corner rules and temporary
   obstacles.
2. **Offline atlas and road planner** (next): a precomputed, versioned world
   graph of connectors (exits, seams, chokepoints) and roads, searched like a
   car navigation system that prefers highways for most of a long journey and
   returns waypoints instead of every tile.
3. **Group movement**: flow fields shared by many monsters or NPCs heading for
   one goal.

## API

The contract is [`proto/atrinik/pathfinder/v1/pathfinder.proto`](proto/atrinik/pathfinder/v1/pathfinder.proto)
(package `atrinik.pathfinder.v1`). Generated Go bindings are committed under
`gen/go/`. The service also implements the standard `grpc.health.v1` health
protocol.

- `FindPath(start, goal_tile | goal_map, agent, blocked, max_expanded)` returns
  the shortest steps (`START`, `WALK`, `SEAM`, `EXIT`) and their cost. Every move costs
  one, diagonal or not; so do a seam crossing and an exit. An automatic exit
  (triggered by stepping on it) is reported as the walk onto its tile followed
  by the exit; an applied exit is used from the tile the agent stands on.
  Nobody can stop on an automatic exit, so it is never a goal tile (ask for
  its destination map instead); a path that starts on one, or arrives on one
  through another exit, continues through it.
- `AgentProfile` selects the terrain bits the agent can stand on (walking by
  default), the keys it carries, the diagonal corner policy (`STRICT` by
  default: both orthogonal neighbours must be passable), whether it must stay
  on its map (monsters and NPCs), and maps to avoid.
- `DescribeMap` lists a map's size, walkable tiles, seams and exits.
  `GetWorldInfo` reports the loaded maps, tiles, transitions and a SHA-256
  digest of the content files.

## Content model

The reader parses the authored text formats directly: archetypes from
`arch/**/*.arc` (`Object` … `end` blocks, `More` multipart parts) and maps
from extension-less files under `maps/` (an `arch map` header, then `arch` …
`end` objects with nested inventories). A map object's attributes override its
archetype's. The movement model compiles:

- **terrain**: the union of every object's `terrain_type` bits on a tile; a
  tile without any is void;
- **blockers**: `no_pass 1` objects, including every part of a multipart
  archetype, except doors (types 20 and 64) and exits (type 66), which can be
  passed;
- **locks**: a door's `slaying` names the key that opens it;
- **exits** (type 66): `slaying` names the destination (relative to the map's
  directory unless absolute) and `hp`/`sp` the arrival tile, defaulting to the
  destination's `enter_x`/`enter_y`; `walk_on`/`fly_on` make it automatic.
  A destination-less stair with `last_heal` 9 (up) or 10 (down) leads to the
  same tile of `tile_path_9`/`tile_path_10` or of the coordinate map one level
  up or down;
- **seams**: `tile_path_1` … `tile_path_8` (north, east, south, west, then the
  four diagonals), and for coordinate-named maps such as `world_1_50` or
  `world_6_48_-2` (two or three integers of at most three digits ending the
  name) the existing neighbours of the same level.

A map without a positive `width` and `height` fails the load with its path.

## Build and run

Go 1.26.9 (see `.go-version`) is the only build requirement.

```sh
go build ./cmd/pathfinderd
./pathfinderd -content ../content -listen 127.0.0.1:7843
```

The server listens on loopback by default. Each search uses scratch space
proportional to the world (about 17 bytes per tile, roughly 35 MB for the
current content). At most `-max-concurrent` searches (the CPU count by
default) run at once, each with its own retained scratch; further requests
are rejected with `RESOURCE_EXHAUSTED` rather than allocating more. A
canceled or expired request stops its search.

## Development

```sh
sh tools/install-ci-dependencies.sh   # pinned buf, protoc plugins, linters
sh tools/generate.sh                   # after editing proto/
sh tools/validate.sh                   # everything CI checks
PATHFINDER_CONTENT=../content go test ./internal/search -run RealContent -v
```

On the current content (3,660 maps, 2.08 million tiles), loading takes about
a second; the 149-step walk from the Asteria inn to the Strakewood Island
farm takes about 6 ms.

Contributions follow [CONTRIBUTING.md](CONTRIBUTING.md). Provenance is recorded
in [PROVENANCE.md](PROVENANCE.md) and dependencies in
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md). The project is MIT licensed
([LICENSE](LICENSE)).
