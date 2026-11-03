#!/usr/bin/env bash
#
# Applies or checks the AIX patches to vendored dependencies.
#
# `go mod vendor` regenerates vendor/ from the module cache and drops these
# changes, so run `hack/aix/vendor-patch.sh apply` after every `make update`
# or `go mod vendor`. If the patch no longer applies after a dependency bump,
# refresh it against the new vendored code (see docs/aix.md).
#
# Usage: hack/aix/vendor-patch.sh apply|check
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
patch=hack/aix/vendor-aix.patch

case "${1:-check}" in
apply)
	if git apply --check -R "$patch" 2>/dev/null; then
		echo "AIX vendor patch already applied"
		exit 0
	fi
	git apply "$patch"
	echo "Applied $patch"
	;;
check)
	if ! git apply --check -R "$patch" 2>/dev/null; then
		echo "AIX vendor patch is not applied; run: hack/aix/vendor-patch.sh apply" >&2
		exit 1
	fi
	echo "AIX vendor patch is applied"
	;;
*)
	echo "usage: $0 apply|check" >&2
	exit 2
	;;
esac
