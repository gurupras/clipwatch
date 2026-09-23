#!/usr/bin/env bash
# Runs the on-device tests against THIS machine's clipboard. They overwrite what
# is on it, so run this on a test machine, never on one somebody is using.
#
#   scripts/ondevice.sh [extra go test args]
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
CLIPBOARD_ONDEVICE=1 go test -count=1 -v -timeout 5m -run OnDevice "$@" ./...
