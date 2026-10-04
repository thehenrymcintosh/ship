// Package web embeds the UI templates and static assets. There is
// no build step: htmx, its SSE extension and elkjs are vendored in static/.
package web

import "embed"

// Templates holds web/templates/*.html.
//
//go:embed templates/*.html
var Templates embed.FS

// Static holds web/static/*.
//
//go:embed static/*
var Static embed.FS
