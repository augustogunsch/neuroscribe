#!/usr/bin/env python3
"""Pull the README's drawing examples out into JSON for the harness.

scripts/pipeline-harness.html compiles every one of them. It reads them from
here rather than carrying its own copies, because copies drift and the point is
to check what people are actually told to type — an example that no longer
draws is a documentation bug, and one nobody would notice by reading.

    python3 scripts/readme-examples.py

Run it after editing the Drawing section. The harness reports a mismatch in
count, but a changed body in a stale file it cannot see.
"""
import json
import os
import re
import sys

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FENCE = re.compile(r'^```(plot|python plot)\n(.*?)^```$', re.S | re.M)


def main():
    with open(os.path.join(HERE, "README.md"), encoding="utf-8") as f:
        readme = f.read()

    try:
        start = readme.index("\n## Drawing\n")
        end = readme.index("\n## How offline works\n", start)
    except ValueError:
        raise SystemExit("cannot find the Drawing section; its headings moved")

    blocks = [{"lang": lang, "code": body}
              for lang, body in FENCE.findall(readme[start:end])]
    if not blocks:
        raise SystemExit("the Drawing section has no fenced examples in it")

    out = os.path.join(HERE, "scripts", "readme-examples.json")
    with open(out, "w", encoding="utf-8") as f:
        json.dump(blocks, f, indent=1)
        f.write("\n")

    for b in blocks:
        first = next((l for l in b["code"].splitlines()
                      if l.strip() and not l.strip().startswith(("#:", "//:"))), "")
        print(f"  {b['lang']:12} {first[:60]}")
    print(f"{len(blocks)} examples -> scripts/readme-examples.json")


if __name__ == "__main__":
    sys.exit(main())
