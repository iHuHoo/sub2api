#!/usr/bin/env bash
# Create the GitHub Release for an Aox tag once its image is published.
# An existing Release is left untouched, so reruns are safe.
# Usage: aox-release.sh <tag> <image> <digest> [extra gh release create flags...]
set -euo pipefail
tag=${1:?tag} image=${2:?image} digest=${3:?digest}
shift 3

[[ $tag =~ ^v[0-9]+\.[0-9]+\.[0-9]+-aox\.[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "invalid Aox tag: $tag" >&2; exit 1; }
[[ $digest =~ ^sha256:[0-9a-f]{64}$ ]] || { echo "invalid image digest: $digest" >&2; exit 1; }

if gh release view "$tag" >/dev/null 2>&1; then
  echo "Release $tag already exists; leaving it unchanged."
  exit 0
fi

notes="Image: \`$image:$tag\`
Digest: \`$digest\`

A Release means the image was built and published. It does not mean the version is deployed."

gh release create "$tag" --verify-tag --title "$tag" --generate-notes --notes "$notes" "$@"
