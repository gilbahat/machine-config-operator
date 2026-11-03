#!/usr/bin/env bash
#
# Cross-compiles machine-config-daemon for AIX (POWER, 64-bit XCOFF).
# Output: _output/aix/ppc64/machine-config-daemon
#
# containers_image_openpgp replaces the cgo gpgme signature backend with the
# pure-Go one; AIX builds are CGO_ENABLED=0 like the other reuna-labs AIX ports.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
hack/aix/vendor-patch.sh check

GOOS=aix GOARCH=ppc64 CGO_ENABLED=0 \
	WHAT_PATH=cmd/machine-config-daemon WHAT=machine-config-daemon \
	GOTAGS="containers_image_openpgp ${GOTAGS:-}" \
	hack/build-go.sh
