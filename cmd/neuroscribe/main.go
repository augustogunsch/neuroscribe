// Neuroscribe: a self-hosted, end-to-end encrypted knowledge base.
//
// Everything is in internal/app. This file exists because Go needs a package
// main somewhere, and keeping it empty means nothing has to be reached through
// it — the whole program is importable, and therefore testable, as a library.
package main

import "neuroscribe/internal/app"

func main() {
	app.Main()
}
