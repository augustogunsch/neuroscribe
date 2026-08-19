# syntax=docker/dockerfile:1
#
# Neuroscribe container image.
#
# The runtime holds a single static binary plus (optionally) a TeX
# installation for PDF export. It runs as an unprivileged user, expects a
# read-only root filesystem, and needs exactly one writable place: /data,
# where the SQLite database lives. See compose.yaml for the hardened runtime
# flags that go with it.

# ---- build ----------------------------------------------------------------
# Pinned by digest, not just tag: a tag is repointable, and the browser assets
# are already SHA-pinned, so the compiler should be too. Digest = golang
# 1.26.6-bookworm; bump tag and digest together (docker buildx imagetools
# inspect golang:<tag> prints the new digest).
FROM golang:1.26.6-bookworm@sha256:116d58cbd88c1297624acc6e967a060012422bacf9930927e23fb719189c6f36 AS build
WORKDIR /src

# dependencies first, so code edits do not re-download the module cache
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
# CGO stays off: modernc.org/sqlite is pure Go, so the binary is static.
# The cache mounts matter more here than usual: that same pure-Go SQLite is
# minutes of compile time, and without them every image rebuild pays it again.
RUN --mount=type=cache,target=/go/pkg/mod \
	--mount=type=cache,target=/root/.cache/go-build \
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/neuroscribe .

# ---- runtime --------------------------------------------------------------
# Digest = debian:bookworm-slim; bump tag and digest together.
FROM debian:bookworm-slim@sha256:abd67ffcfa541b485a3dff59865ab629aa048a6c613e639d36e7456b0b229241 AS runtime

# Nothing here executes user code or reads a note. Python and JavaScript
# snippets run in the browser, and so does typesetting: the ~1.3 GB TeX Live
# install this image used to carry is gone, replaced by a WebAssembly
# typesetter served to the page — see compose.yaml for how both are mounted in.

RUN set -eux; \
	apt-get update; \
	apt-get install -y --no-install-recommends ca-certificates tini; \
	rm -rf /var/lib/apt/lists/*

RUN useradd --system --uid 10001 --user-group --home-dir /data \
		--shell /usr/sbin/nologin neuroscribe \
	&& mkdir -p /data && chown 10001:10001 /data

COPY --from=build /out/neuroscribe /usr/local/bin/neuroscribe

USER 10001:10001
WORKDIR /data
VOLUME ["/data"]

ENV NEUROSCRIBE_DB=/data/neuroscribe.db \
	NEUROSCRIBE_ADDR=0.0.0.0:8484 \
	NEUROSCRIBE_PYODIDE_DIR=/opt/pyodide \
	NEUROSCRIBE_TYPST_DIR=/opt/typst
EXPOSE 8484

# the binary probes itself, so the image needs no curl or wget
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
	CMD ["/usr/local/bin/neuroscribe", "healthcheck"]

# tini reaps zombies and forwards signals for a clean shutdown
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/neuroscribe"]
