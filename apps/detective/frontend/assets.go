// Package frontend embeds the built desktop interface.
package frontend

import "embed"

// Assets contains the Vite output. Run npm run build before building the app.
//
//go:embed all:dist
var Assets embed.FS
