#!/usr/bin/env python3
"""Verify that every relative markdown link points at a file that exists.

The v2 docs are a hand-maintained web of ./relative.md links; a renamed or
moved page breaks them silently, and nothing else in CI reads markdown.

Only relative links are checked. External URLs are left alone on purpose:
their reachability is not this repository's problem, and a flaky third-party
host must never be able to fail a build.

Fenced blocks and inline code spans are stripped before links are extracted.
Without that, every Go snippet in the docs registers as a broken link --
`handlers[name](ctx)` is indistinguishable from `[name](ctx)` to a regex that
has not been told where the code is.
"""

from __future__ import annotations

import os
import re
import sys

# The docs are Thai; a cp1252 console would otherwise die on the first error it
# tries to print rather than on the link it found.
if hasattr(sys.stdout, "reconfigure"):
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

FENCE = re.compile(r"^\s*(```|~~~)")
INLINE_CODE = re.compile(r"`[^`\n]*`")
LINK = re.compile(r"\]\(\s*<?([^)\s>]+)>?(?:\s+[\"'][^\"']*[\"'])?\s*\)")
# Anything with a scheme, protocol-relative, or a bare in-page #anchor.
EXTERNAL = re.compile(r"^([a-z][a-z0-9+.\-]*:|//|#)", re.IGNORECASE)

SKIP_DIRS = {".git", "node_modules", "dist", ".idea", ".vscode"}


def markdown_files() -> list[str]:
    found = []
    for dirpath, dirnames, filenames in os.walk(ROOT):
        dirnames[:] = [d for d in dirnames if d not in SKIP_DIRS]
        for name in filenames:
            if name.endswith(".md"):
                found.append(os.path.join(dirpath, name))
    return sorted(found)


def strip_code(text: str) -> list[tuple[int, str]]:
    """Return (line number, prose) for every line outside a fenced block."""
    lines = []
    in_fence = False
    for number, line in enumerate(text.splitlines(), start=1):
        if FENCE.match(line):
            in_fence = not in_fence
            continue
        if in_fence:
            continue
        lines.append((number, INLINE_CODE.sub("", line)))
    return lines


def main() -> int:
    broken: list[str] = []
    checked = 0

    for path in markdown_files():
        rel = os.path.relpath(path, ROOT).replace(os.sep, "/")
        directory = os.path.dirname(path)

        with open(path, encoding="utf-8") as handle:
            text = handle.read()

        for number, line in strip_code(text):
            for target in LINK.findall(line):
                if EXTERNAL.match(target):
                    continue

                # Drop a trailing #anchor: the file is what is being checked,
                # and heading anchors are generated differently by every
                # renderer.
                filename = target.split("#", 1)[0]
                if not filename:
                    continue

                checked += 1
                # A leading slash means repository-root-relative here; markdown
                # rendered on GitHub resolves it against the site, not the repo,
                # so those are reported too.
                base = ROOT if filename.startswith("/") else directory
                resolved = os.path.join(base, filename.lstrip("/"))

                if not os.path.exists(resolved):
                    print(f"::error file={rel},line={number}::broken link -> {target}")
                    broken.append(f"  {rel}:{number} -> {target}")

    if broken:
        print(f"\n{len(broken)} broken relative link(s) out of {checked} checked:\n")
        print("\n".join(broken))
        return 1

    print(f"All {checked} relative markdown links resolve.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
