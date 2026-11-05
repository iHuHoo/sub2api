#!/bin/sh
# Release archives and install.sh ship the Claude client profile the server needs at
# startup (#144). Runs install.sh's install_client_profile against an extracted layout.
set -eu
cd "$(dirname "$0")/../.."
fail() { printf 'release client profile test failed: %s\n' "$1" >&2; exit 1; }

grep -q 'src: backend/resources/claude-client-profile/profile.json' .goreleaser.yaml || fail 'archive does not include the profile'
grep -q 'dst: resources/claude-client-profile' .goreleaser.yaml || fail 'archive path is not resources/claude-client-profile'
grep -q 'install_client_profile "\$TEMP_DIR" "\$INSTALL_DIR"' deploy/install.sh || fail 'download_and_extract does not install the profile'

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
{
  echo 'print_success() { :; }'
  echo 'print_warning() { echo "WARN $*"; }'
  sed -n '/^install_client_profile() {/,/^}/p' deploy/install.sh
} > "$work/fn.sh"
. "$work/fn.sh"

archive="$work/archive"; install="$work/opt"
mkdir -p "$archive/resources/claude-client-profile" "$install"
cp backend/resources/claude-client-profile/profile.json "$archive/resources/claude-client-profile/profile.json"

install_client_profile "$archive" "$install"
cmp -s "$archive/resources/claude-client-profile/profile.json" "$install/resources/claude-client-profile/profile.json" || fail 'fresh install did not copy the profile'
[ ! -e "$install/resources/claude-client-profile/profile.json.backup" ] || fail 'fresh install made a backup'

install_client_profile "$archive" "$install"
[ ! -e "$install/resources/claude-client-profile/profile.json.backup" ] || fail 'unchanged profile made a backup'

echo '{"old":true}' > "$install/resources/claude-client-profile/profile.json"
install_client_profile "$archive" "$install"
grep -q '"old":true' "$install/resources/claude-client-profile/profile.json.backup" || fail 'changed profile was not backed up'
cmp -s "$archive/resources/claude-client-profile/profile.json" "$install/resources/claude-client-profile/profile.json" || fail 'upgrade did not replace the profile'

out=$(install_client_profile "$work/empty" "$install")
echo "$out" | grep -q 'WARN' || fail 'missing profile in archive is not reported'

printf 'release client profile test passed\n'
