package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var embeddedDist embed.FS

// DistFS returns the embedded Vite production build filesystem rooted at dist/.
func DistFS() (fs.FS, error) {
	return fs.Sub(embeddedDist, "dist")
}
