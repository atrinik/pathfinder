# Atrinik pathfinder guide

- This repository owns the MIT pathfinding and navigation service: the gRPC
  contract in `proto/atrinik/pathfinder/v1`, its committed Go bindings in
  `gen/go`, the content reader (`internal/content`), the compiled movement
  model (`internal/world`), the search engine (`internal/search`), the atlas
  and waypoint planner (`internal/atlas`, file format in
  `proto/atrinik/pathfinder/atlas/v1`), the gRPC service (`internal/server`)
  and the `pathfinderd` and `atlas` commands.
- Clean room: this is an independent MIT implementation. Never read, copy,
  port or paraphrase GPL sources (the Classic server, libatrinik, Classic
  client or their tests) for this repository. Permitted inputs are authored
  content as data, documented and observed interoperability facts, the MIT
  playtester's compatibility facts, published specifications and permissively
  licensed dependencies. Record the inputs of every new behaviour group in
  `PROVENANCE.md`.
- The API is versioned: changes to `v1` must stay backwards compatible (`buf
  breaking` against the latest release). After editing a `.proto`, run
  `tools/generate.sh` with the pinned plugins and commit the regenerated code.
- Keep per-query work allocation-free on hot paths and bounded by
  `max_expanded`; the service must stay safe for concurrent use.
- The atlas file is versioned by `atlas.FileFormat`; bump it for any change
  to what a file means, and keep loading fail-closed (digest, map list and
  graph checks). The planner must keep matching the precise search's routes
  and costs on road- and seam-free worlds
  (`TestPlansMatchExactSearchWithoutRoads`).
- Validate with `tools/validate.sh` (formatting, vet, staticcheck, tests with
  the race detector, buf lint and generated-code freshness, govulncheck) and
  `git diff --check`. Real-content checks run when `PATHFINDER_CONTENT` names
  a content checkout.
- Commits and pull-request titles use Conventional Commits. Merging is the
  maintainers' decision.
