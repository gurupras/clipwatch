#!/usr/bin/env bash
# Builds the on-device test binary for a machine and runs it there over ssh.
# The tests overwrite that machine's clipboard: point this at a test VM.
#
#   scripts/ondevice-remote.sh user@host [GOOS] [GOARCH]
#
# GOOS defaults to linux. The binary is self-contained (this package uses no
# cgo), so the target needs no Go toolchain — only a desktop session, because a
# clipboard belongs to one:
#   Linux    DISPLAY (or WAYLAND_DISPLAY) must point at the session's server
#   Windows  the interactive session owns the clipboard; an ssh session has its
#            own window station and cannot see it, so run the binary through
#            whatever puts a command in the console session
#   macOS    the pasteboard belongs to the logged-in GUI session
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
target="${1:?usage: ondevice-remote.sh user@host [GOOS] [GOARCH]}"
goos="${2:-linux}"
goarch="${3:-amd64}"
bin="clipboard.ondevice.test"
[ "$goos" = windows ] && bin="$bin.exe"

echo "== building $bin for $goos/$goarch"
GOOS="$goos" GOARCH="$goarch" go test -c -o "/tmp/$bin" .

echo "== copying to $target"
scp ${SSH_OPTS:-} -q "/tmp/$bin" "$target:$bin"

echo "== running on $target"
# shellcheck disable=SC2029  # the remote command is meant to expand here
ssh ${SSH_OPTS:-} "$target" "CLIPBOARD_ONDEVICE=1 ${REMOTE_ENV:-} ./$bin -test.run OnDevice -test.v -test.timeout 5m"
