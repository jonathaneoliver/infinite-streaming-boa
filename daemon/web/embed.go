// Package web carries the compiled Vue interface, embedded into the daemon
// binary so the Pi ships as a single file with nothing to install at runtime.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the UI rooted at dist/, so paths resolve as "/index.html" rather
// than "/dist/index.html".
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // impossible: the directory is embedded at compile time
	}
	return sub
}
