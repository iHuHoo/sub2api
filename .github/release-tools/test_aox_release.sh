#!/usr/bin/env bash
set -euo pipefail
script="$(cd "$(dirname "$0")" && pwd)/aox-release.sh"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# Fake gh: `release view` succeeds only when $work/exists is present; every call is logged.
mkdir "$work/bin"
cat >"$work/bin/gh" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$*" >>"$work/calls"
[[ "\$1 \$2" == "release view" ]] && { [[ -e "$work/exists" ]]; exit; }
exit 0
EOF
chmod +x "$work/bin/gh"
export PATH="$work/bin:$PATH"

fail() { echo "aox-release test failed: $1" >&2; exit 1; }
tag=v0.2.19-aox.0.0.36
digest=sha256:$(printf '%064d' 7)

: >"$work/calls"
"$script" "$tag" ghcr.io/o/r "$digest" >/dev/null
grep -q "^release create $tag --verify-tag --title $tag --generate-notes --notes " "$work/calls" || fail "missing create"
grep -q "^Digest: \`$digest\`$" "$work/calls" || fail "digest missing from notes"

touch "$work/exists"; : >"$work/calls"
"$script" "$tag" ghcr.io/o/r "$digest" >/dev/null
! grep -q 'release create' "$work/calls" || fail "existing release was recreated"

rm "$work/exists"; : >"$work/calls"
! "$script" "$tag" ghcr.io/o/r sha256:bad 2>/dev/null || fail "bad digest accepted"
! "$script" v1.2.3 ghcr.io/o/r "$digest" 2>/dev/null || fail "non-Aox tag accepted"
[[ ! -s "$work/calls" ]] || fail "gh called for invalid input"

# Push and dispatch of one tag must share a group regardless of the unused version input.
workflow="$(dirname "$script")/../workflows/build-aox.yml"
grep -qx '  group: build-aox-${{ github.ref }}' "$workflow" || fail "tag builds do not share one concurrency group"

echo "aox-release tests passed"
