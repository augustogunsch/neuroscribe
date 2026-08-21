#!/usr/bin/env python3
"""Serve the repository for scripts/pipeline-harness.html.

`python3 -m http.server` is not enough, and the way it is not enough is quiet:
the page loads, the typesetter works, and every Python figure fails with
"Failed to fetch" from inside Pyodide.

Two reasons, both of which the real server (internal/app/runner.go) handles.

Snippets run in a sandboxed frame, which has an opaque origin, so its requests
back to this server are cross-origin — and a plain file server answers them
without the headers that permit that. Pyodide's own fetches for its stdlib and
lockfile are the first to fail.

And http.server is single-threaded. Pyodide asks for several files at once, and
a server that can only answer one at a time deadlocks against a page waiting on
all of them.

Nothing is cached, either, which is not how the real server behaves but is what
a harness wants: a stale file means the run reports on code that is no longer
on disk, and that has already nearly hidden a failing mutation once.

    python3 scripts/harness-serve.py [port]
    open http://127.0.0.1:8791/scripts/pipeline-harness.html

It needs ./static (a link to web/static), ./typst and ./pyodide beside it:

    ln -sfn web/static static
"""
import sys
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer


class Handler(SimpleHTTPRequestHandler):
    def end_headers(self):
        # what runner.go sends, for the same reason: the frame that runs
        # snippets is sandboxed, and sandboxed means a different origin
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Cross-Origin-Resource-Policy", "cross-origin")
        self.send_header("Cache-Control", "no-store")
        super().end_headers()

    def log_message(self, format, *args):  # noqa: A002 - base class name
        # only the failures; a Pyodide boot is several hundred requests
        if not str(args[1] if len(args) > 1 else "").startswith("2"):
            super().log_message(format, *args)


if __name__ == "__main__":
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 8791
    print(f"harness server on http://127.0.0.1:{port}  (non-2xx logged below)")
    ThreadingHTTPServer(("127.0.0.1", port), Handler).serve_forever()
