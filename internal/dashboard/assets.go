// Package dashboard exposes the production web application embedded in the
// Rehearse binary.
package dashboard

import (
	"embed"
	"fmt"
	"io/fs"
	"net/http"
)

//go:embed dist
var assets embed.FS

// Handler returns an HTTP handler for the embedded production dashboard.
func Handler() (http.Handler, error) {
	content, err := fs.Sub(assets, "dist")
	if err != nil {
		return nil, fmt.Errorf("open embedded dashboard: %w", err)
	}

	if _, err := fs.Stat(content, "index.html"); err != nil {
		return nil, fmt.Errorf("find embedded dashboard entrypoint: %w", err)
	}

	return http.FileServer(http.FS(content)), nil
}
