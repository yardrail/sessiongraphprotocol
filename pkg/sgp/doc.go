// Package sgp implements the Session Graph Protocol (SGP).
//
// SGP models an agent session as an append-only directed acyclic graph of
// immutable message nodes. Canonical parent links define the resumable history,
// while audit links preserve branch, rewrite, and subagent provenance.
package sgp
