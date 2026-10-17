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

- Textbook A* search with a consistent heuristic (Chebyshev distance on the
  goal map, zero elsewhere) over a binary heap, and generation-stamped scratch
  arrays. Movement rules (eight-way unit-cost moves, seams, exits) follow the
  compatibility facts above; the diagonal corner policy is an explicit agent
  option rather than an assumed server rule.

### `internal/server`, `cmd/pathfinderd`

- The gRPC service and health protocol, written against the public
  `google.golang.org/grpc` APIs.
