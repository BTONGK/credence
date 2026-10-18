// Package ui embeds the built web UI (ui/dist, produced by `npm run build`
// in ui/). The Go binary serves it, so the UI needs no separate install.
package ui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the built UI files.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
