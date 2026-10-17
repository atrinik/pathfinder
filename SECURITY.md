# Security policy

Report vulnerabilities privately through GitHub Security Advisories for
`atrinik/pathfinder`. Do not include credentials, private player data or
production state in a public issue.

`pathfinderd` binds to loopback (`127.0.0.1:7843`) by default and has no
authentication or transport security of its own. Expose it beyond the host
only behind an authenticated, encrypted channel that you control.

Every request is bounded: a search expands at most `-max-expanded` states, and
one request may name at most 4,096 temporary blocked tiles and 1,024 keys or
avoided maps. Requests naming unknown maps or out-of-range coordinates are
rejected. Content is loaded once at startup from a trusted checkout; the
service never writes to it.
