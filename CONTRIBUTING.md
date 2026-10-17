# Contributing to the pathfinder

All implementation in this repository is independently written under MIT.
Human-written and agent-assisted contributions follow the same review,
provenance, testing and validation requirements.

## Permitted design inputs

You may use authored Atrinik content as data (maps, archetypes), black-box
observable game behaviour, interoperability facts such as file formats and
naming conventions, public specifications and literature (A*, jump point
search, hierarchical and contraction-hierarchy routing, flow fields), the MIT
playtester's recorded compatibility facts, and permissively licensed
dependencies.

Do not consult GPL sources for this repository: the Classic server,
libatrinik, the Classic client, their tests or fixtures. Unadmitted GPL
material must not be copied, transliterated, mechanically translated or
structurally rewritten here, and no GPL or AGPL dependency may be added.

## Provenance

Every new module or behaviour group adds or updates `PROVENANCE.md` with the
inputs it was built from.

## Changes

- Use Conventional Commits for commits and pull-request titles, for example
  `feat(search): add jump point search`.
- Keep `v1` of the API backwards compatible and regenerate bindings with
  `tools/generate.sh`.
- Run `tools/validate.sh` before opening a pull request.
