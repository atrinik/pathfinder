# Atrinik pathfinder

`pathfinder` is an MIT-licensed, clean-room pathfinding and navigation service
for Atrinik. It loads an authored content checkout and answers movement
queries over gRPC, so any program can share one implementation: the Classic
server, the playtester bot, or the next-generation server. Talking to it over
the network keeps its MIT code separate from GPL clients that consult it.

The service is being built in stages:

1. **Precise pathfinding**: exact tile-by-tile paths for one agent, across
   tiled-map seams, exits and stairs, honouring terrain, static blockers,
   locked doors, diagonal-corner rules and temporary obstacles.
2. **Offline atlas and road planner**: a precomputed, versioned world graph
   of connectors (exits, arrivals and seam crossings) joined by road-weighted
   walks, searched like a car navigation system that prefers highways for
   most of a long journey, and returning waypoints instead of every tile.
3. **Group movement** (next): flow fields shared by many monsters or NPCs
   heading for one goal.

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
- `Plan(start, goal_tile | goal_map, avoided_maps, spacing)` returns a
  route as sparse waypoints for a plain walker (walking terrain, no keys,
  strict corners): `START`, then `WALK` points to click-to-move between,
  `EXIT` tiles to apply (or step onto, when `automatic`), `SEAM` border tiles
  to step across, `ARRIVE` where those land, and `GOAL`. Consecutive
  waypoints on one map are at most `spacing` tiles apart (7 by default), and
  walking straight between them stays within a tile of the planned route
  (half a tile on roads, whose entry and exit points are always kept), and
  every segment between two waypoints can be walked in a straight line. A
  plan that starts on an automatic exit begins by taking it, and one that
  starts on an exit inside a wall can still use it. `max_expanded` bounds a
  plan's work, counting each connector expanded, edge examined, tile
  searched and waypoint returned (capped by the server's `-max-expanded`); hitting it returns
  `LIMIT_REACHED`.
  Roads cost two thirds of a plain step, so the route follows them where the
  detour pays; `cost` is in thirds of a move and `tiles` counts the moves.
  Agents with other abilities (keys, swimming, staying on one map) use
  `FindPath`.
- `GetAtlasInfo` reports the atlas' connectors, edges, road tiles, content
  digest and whether it was built at startup or loaded from a file.
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

## Atlas

The atlas (`internal/atlas`) is the planner's road network:

- **roads** are tiles holding a floor archetype from `floors/flagstone/`,
  `floors/bridge_wood/` or `floors/snow/snow_path/`;
- **connectors** are the tiles where travel leaves or enters an area: exit
  tiles, exit arrivals, and one crossing per run of border tiles that joins
  the same two walkable components across a seam (diagonal crossings need
  both corner tiles passable, and a crossing onto an automatic exit is its
  own connector);
- **edges** are exits and seam crossings (cost 3), and walks between every
  pair of connectors in one walkable component of a map, priced by a
  road-weighted Dijkstra search (2 per step between road tiles, 3 otherwise).

A plan searches only the connector graph, starting from the connectors the
start can walk to and finishing at the goal (or at any connector on the goal
map), then re-derives the tile path of each walk it uses and simplifies it
into waypoints (Ramer-Douglas-Peucker with a one-tile tolerance, split to the
requested spacing). Without roads and seams it finds exactly the precise
search's routes at the same cost; a seam is crossed at the middle of its run.

`pathfinderd` builds the atlas at startup unless `-atlas` names a prebuilt
file from the `atlas` command (format
[`proto/atrinik/pathfinder/atlas/v1/atlas.proto`](proto/atrinik/pathfinder/atlas/v1/atlas.proto)).
A file is stamped with the content digest and map list and is refused for any
other content, and carries a SHA-256 digest of its connector graph that loading
verifies. Loading also recomputes the connectors, exits and seams (they
follow cheaply from the content) and requires the file to match them exactly;
only the costly walks are read: exactly one from each connector to every
other connector walkable from its component, so a damaged file is refused
rather than silently losing or inventing routes.
A plan also checks each walk it re-derives against its edge's cost and fails
with `DATA_LOSS` on a mismatch. Files are byte-for-byte reproducible. Like path searches, at most
`-max-concurrent` plans run at once, each reusing its own scratch space, and
a canceled request stops its plan.

## Build and run

Go 1.26.9 (see `.go-version`) is the only build requirement.

```sh
go build ./cmd/pathfinderd ./cmd/atlas
./atlas -content ../content -out content.atlas      # optional
./pathfinderd -content ../content -atlas content.atlas -listen 127.0.0.1:7843
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
PATHFINDER_CONTENT=../content go test ./internal/search ./internal/atlas -run RealContent -v
```

On the current content (3,660 maps, 2.08 million tiles), loading takes about
a second; the 149-step walk from the Asteria inn to the Strakewood Island
farm takes about 6 ms. The atlas has 8,915 connectors, 74,277 edges and
35,960 road tiles. Building it takes 35 ms on 32 cores and 250 ms on one;
loading its 900 KB file takes 29 ms and 50 ms. Planning the inn-to-farm trip
takes about 0.6 ms (72 waypoints over 163 tiles), and the 447-step walk from
Strakewood town to the farm about 1.8 ms instead of 28 ms for the precise
search, with 14 allocations from a reused planner slot; roads lengthen these
walks by 5 to 10 %.

Contributions follow [CONTRIBUTING.md](CONTRIBUTING.md). Provenance is recorded
in [PROVENANCE.md](PROVENANCE.md) and dependencies in
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md). The project is MIT licensed
([LICENSE](LICENSE)).
