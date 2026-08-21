"""Prepare a fetched Typst package for a compiler that has no filesystem.

Two things have to happen before the browser can use one.

The compiler is handed files one at a time as shadow entries, and a package is
a tree of .typ files importing each other by relative path, so the tree is
listed into files.json at fetch time rather than discovered at runtime.

And a package may import another through the registry — `@preview/cetz:0.3.2`.
There is no registry here and adding one would mean letting the compiler reach
the network, which is the one thing this design does not do: a note is
typeset without anything leaving the browser. So those imports are rewritten
to the path the sibling package is mapped at. The version is dropped with
them, which is a real if small risk — a package asking for 0.3.2 gets whatever
0.3.x was fetched — and is the reason the versions are pinned together in the
Makefile.

    usage: package-manifest.py <package-dir> [<package-dir> ...]
"""
import json
import os
import re
import sys

# where each package's entrypoint is mapped inside the compiler
ENTRYPOINTS = {
    "cetz": "/cetz/src/lib.typ",
    "cetz-plot": "/cetz-plot/src/lib.typ",
    "oxifmt": "/oxifmt/oxifmt.typ",
}

REGISTRY_IMPORT = re.compile(r'"@preview/([a-z0-9-]+):[0-9.]+"')

# A package may also import from its own root: `#import "/src/vector.typ"`.
# Inside a real package that "/" is the package; here every package shares one
# filesystem, so it has to be made relative to where this one is mounted.
#
# The "#" is optional, and that is the whole subtlety. In markup an import is
# written "#import"; inside a code block it is a bare "import". The bare ones
# sit in function bodies, so they resolve when the function is *called* rather
# than when the file is read — which is why missing them looked like a working
# package right up until something was actually drawn with it.
ROOT_IMPORT = re.compile(r'((?:#\s*)?\b(?:import|include)\s+)"(/[^"]*)"')


def rewrite(path, pkg):
    """Make a package's imports work inside one shared, flat filesystem."""
    with open(path, encoding="utf-8") as f:
        before = f.read()

    def to_local(m):
        name = m.group(1)
        if name not in ENTRYPOINTS:
            raise SystemExit(
                f"{path}: imports @preview/{name}, which is not fetched. "
                f"Add it to the typst target in the Makefile and to ENTRYPOINTS here.")
        return '"' + ENTRYPOINTS[name] + '"'

    after = REGISTRY_IMPORT.sub(to_local, before)

    def to_mounted(m):
        target = m.group(2)
        # already pointing at a mounted package (including one just rewritten
        # above), so leave it alone
        if any(target.startswith("/" + p + "/") for p in ENTRYPOINTS):
            return m.group(0)
        return m.group(1) + '"/' + pkg + target + '"'

    after = ROOT_IMPORT.sub(to_mounted, after)

    if after != before:
        with open(path, "w", encoding="utf-8") as f:
            f.write(after)
        return True
    return False


def main(dirs):
    for base in dirs:
        pkg = os.path.basename(base.rstrip("/"))
        if not os.path.isdir(base):
            raise SystemExit(f"missing package directory: {base}")
        files, rewritten = [], 0
        for dirpath, _dirs, names in os.walk(base):
            for n in sorted(names):
                if not n.endswith((".typ", ".toml")):
                    continue
                full = os.path.join(dirpath, n)
                if n.endswith(".typ") and rewrite(full, pkg):
                    rewritten += 1
                files.append(os.path.relpath(full, base))
        files.sort()
        with open(os.path.join(base, "files.json"), "w", encoding="utf-8") as f:
            json.dump(files, f)
        note = f", {rewritten} import(s) localised" if rewritten else ""
        print(f"{pkg}: {len(files)} files{note}")


if __name__ == "__main__":
    if len(sys.argv) < 2:
        raise SystemExit(__doc__)
    main(sys.argv[1:])
