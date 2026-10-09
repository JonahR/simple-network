// Package simplenetwork holds repo-level assets shared by the services.
package simplenetwork

import (
	"embed"
	"io/fs"
)

//go:embed "Network KT"
var knowledgeFS embed.FS

// Knowledge returns the Network KT docs, visuals, and interactive pages, rooted
// at the "Network KT" folder. Every service serves them under /learn/kt/
// (learn.Handler), so every learning link works locally and matches the
// running version.
func Knowledge() fs.FS {
	sub, err := fs.Sub(knowledgeFS, "Network KT")
	if err != nil {
		panic(err) // the folder is embedded at build time, so this can't fail
	}
	return sub
}
