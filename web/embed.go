// Package web is the frontend, and the file that hands it to the server.
//
// It lives beside static/ and templates/ rather than with the Go code because
// go:embed can only reach files at or below its own directory: putting the
// directive anywhere else would mean burying the entire frontend inside a
// package that is otherwise about HTTP.
//
// The paths inside FS are the paths the browser asks for — "static/app.js",
// "templates/app.html" — so everything that reads them reads them by URL.
package web

import "embed"

//go:embed static templates
var FS embed.FS
