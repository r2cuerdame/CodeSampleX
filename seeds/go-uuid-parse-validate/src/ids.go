// Package sample generates and checks UUIDs with github.com/google/uuid.
package sample

import "github.com/google/uuid"

// New returns a random (v4) UUID. uuid.New panics if the system random
// source fails; uuid.NewRandom returns that as an error instead, which is
// what long-running services should use.
func New() uuid.UUID { return uuid.New() }

// Parse accepts the canonical, urn:uuid: and braced forms alike. It
// returns an error for anything else — unlike uuid.MustParse, which panics
// and therefore has no place on an input path.
func Parse(s string) (uuid.UUID, error) { return uuid.Parse(s) }

// IsNil reports the all-zero UUID, the value a failed decode leaves behind.
func IsNil(u uuid.UUID) bool { return u == uuid.Nil }
