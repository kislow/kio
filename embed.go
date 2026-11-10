// Package kio carries the files ki needs at run time that live outside the Go
// packages: the agent instructions and the /ki-* skills. They are embedded so a
// release binary works without a checkout of the repository.
package kio

import "embed"

// Data holds agent/*.md and skills/, laid out exactly as in a checkout.
//
//go:embed agent/*.md skills
var Data embed.FS
