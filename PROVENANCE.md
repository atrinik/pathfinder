# Provenance

All code in this repository was written independently for it under the MIT
License. No GPL source (the Classic server, libatrinik, the Classic client,
or their tests and fixtures) was read, copied, ported or translated while
writing it.

## Inputs by module

### `internal/content` (content reader)

- The archetype and map text formats, read directly from the authored content
  files as data: `Object`/`More`/`end` archetype blocks, `arch`/`end` map
  object blocks with nested inventories, and `msg`/`endmsg` text.
- The meaning of the attributes used (`terrain_type`, `no_pass`, `type` 20,
  64 and 66, `slaying`, `hp`/`sp`, `walk_on`/`fly_on`, `last_heal` 9/10,
  `tile_path_1`…`tile_path_10`, `enter_x`/`enter_y`) and the coordinate-map
  naming convention (`world_X_Y[_Z]`, integers of at most three digits), taken
  from the compatibility facts recorded in the MIT
  [`atrinik/playtester`](https://github.com/atrinik/playtester) world graph.

### `internal/world`, `internal/search` (movement model and search)

- Textbook A* search over a binary heap, with generation-stamped scratch
  arrays held per concurrent search slot. The admissible, consistent
  heuristic was designed for this repository from the movement rules: on the
  goal map, the Chebyshev distance to the goal capped by a re-entry bound (one
  transition plus the Chebyshev distance from the nearest place an exit
  arrives or a neighbouring seam enters the goal map); off the goal map, that
  re-entry bound. Movement rules (eight-way unit-cost moves, seams, exits)
  follow the compatibility facts above; the diagonal corner policy is an
  explicit agent option rather than an assumed server rule.

### `internal/atlas`, `cmd/atlas` (atlas and waypoint planner)

- Textbook techniques: connected-component labelling by breadth-first
  search, Dijkstra searches over tiles and over the connector graph (a
  hierarchical, transit-node style abstraction in which only the tiles where
  travel changes area become nodes), and Ramer-Douglas-Peucker polyline
  simplification. The connector choice, road weights and waypoint rules were
  designed for this repository.
- The road archetype directories (`floors/flagstone/`, `floors/bridge_wood/`,
  `floors/snow/snow_path/`), taken from the authored content's own `arch/`
  layout as data.
- The click-to-move reach behind the default waypoint spacing of 7, from the
  MIT playtester's compatibility facts: click targets must be visible in the
  player-centred map view and off its edge rows, which for the default
  17-tile view leaves 7 tiles in each direction.

### `internal/server`, `cmd/pathfinderd`

- The gRPC service and health protocol, written against the public
  `google.golang.org/grpc` APIs.
