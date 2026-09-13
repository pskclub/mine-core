#!/bin/sh
set -e

# Usage: ./tag <major-line> [level] [--print]
#
#   ./tag v2                -> next patch on the v2 line   (v2.3.4 -> v2.3.5)
#   ./tag v2 minor          -> next minor                  (v2.3.4 -> v2.4.0)
#   ./tag v2 major          -> next major -- refuses, see below
#   ./tag v2 --print        -> prints what it would tag, tagging nothing
#   ./tag v2 minor --print
#
# The repository ships two Go modules:
#   github.com/pskclub/mine-core       -> tagged v1.x.x  (root, maintenance)
#   github.com/pskclub/mine-core/v2    -> tagged v2.x.x  (v2/, active)
# so each major line must be bumped independently. `git describe` cannot be
# used here: on a shared history it returns whichever tag is nearest, which
# would let a v2 release bump the v1 line (and vice versa).
#
# --print is for callers that need to name the release before it exists (a
# changelog step, a release note header). The name has to come from the same
# place the tag itself does, or the two will disagree.

usage() {
    sed -n '3,20p' "$0" | sed 's/^# \{0,1\}//'
    exit 2
}

MAJOR=""
LEVEL="patch"
PRINT_ONLY=""

for arg in "$@"; do
    case "$arg" in
        --print)               PRINT_ONLY=1 ;;
        patch|minor|major)     LEVEL="$arg" ;;
        -h|--help)             usage ;;
        v[0-9]*)               MAJOR="$arg" ;;
        *)                     echo "unknown argument: $arg" >&2; usage ;;
    esac
done

[ -n "$MAJOR" ] || { echo "a major line is required, e.g. ./tag v2" >&2; usage; }

# A major bump is a new module path (github.com/pskclub/mine-core/v3), a new
# directory and a new go.mod -- not a tag this script can invent. It is refused
# rather than silently producing a tag no importer can resolve.
if [ "$LEVEL" = major ]; then
    cat >&2 <<MSG
refusing to bump the major line automatically.

A vN+1 release in Go is a different module: it needs its own directory, its own
go.mod declaring the /vN+1 path, and every internal import rewritten. Create
that first, then tag the first release by hand:

    git tag ${MAJOR}.0.0 && git push origin ${MAJOR}.0.0
MSG
    exit 1
fi

# Bail out early if this commit is already tagged on this major line: releasing
# the same tree twice under two versions is never what was meant.
EXISTING=$(git tag --points-at HEAD | grep "^${MAJOR}\." || true)
if [ -n "$EXISTING" ]; then
    if [ -n "$PRINT_ONLY" ]; then
        echo "$EXISTING" | sed -n '1p'
        exit 0
    fi
    echo "Already tagged on the ${MAJOR} line: $(echo "$EXISTING" | tr '\n' ' ')"
    exit 0
fi

# Highest tag of this major line, e.g. v1.4.90. --sort=-v:refname orders by
# version, not lexically, so v1.4.90 beats v1.4.9 rather than the other way.
LATEST=$(git tag --list "${MAJOR}.*.*" --sort=-v:refname | head -n 1)

if [ -z "$LATEST" ]; then
    NEW_TAG="${MAJOR}.0.0"
else
    REST="${LATEST#${MAJOR}.}"   # 4.90
    MINOR="${REST%%.*}"          # 4
    PATCH="${REST#*.}"           # 90
    PATCH="${PATCH%%-*}"         # 90 (drop any pre-release suffix)
    PATCH="${PATCH%%.*}"

    case "$LEVEL" in
        minor) MINOR=$((MINOR + 1)); PATCH=0 ;;
        patch) PATCH=$((PATCH + 1)) ;;
    esac
    NEW_TAG="${MAJOR}.${MINOR}.${PATCH}"
fi

if [ -n "$PRINT_ONLY" ]; then
    echo "$NEW_TAG"
    exit 0
fi

echo "Latest ${MAJOR} tag: ${LATEST:-<none>}"
echo "Tagging ${NEW_TAG} (${LEVEL})"

git tag -a "$NEW_TAG" -m "$NEW_TAG"
git push origin "$NEW_TAG"
