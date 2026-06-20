#!/usr/bin/env bash
set -euo pipefail

part="${1:-}"
version_file="${2:-VERSION}"

if [[ ! "$part" =~ ^(major|minor|patch)$ ]]; then
    echo "usage: $0 major|minor|patch [VERSION_FILE]" >&2
    exit 2
fi

if [[ ! -f "$version_file" ]]; then
    echo "version file not found: $version_file" >&2
    exit 1
fi

version="$(tr -d '[:space:]' < "$version_file")"
if [[ ! "$version" =~ ^([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
    echo "invalid SemVer in $version_file: $version" >&2
    exit 1
fi

major="${BASH_REMATCH[1]}"
minor="${BASH_REMATCH[2]}"
patch="${BASH_REMATCH[3]}"

case "$part" in
major)
    major=$((major + 1))
    minor=0
    patch=0
    ;;
minor)
    minor=$((minor + 1))
    patch=0
    ;;
patch)
    patch=$((patch + 1))
    ;;
esac

next="${major}.${minor}.${patch}"
printf '%s\n' "$next" > "$version_file"
printf '%s\n' "$next"
