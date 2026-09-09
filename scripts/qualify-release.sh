#!/bin/sh
set -eu

usage() {
	cat <<'EOF'
Usage: scripts/qualify-release.sh [--container] [--output DIR]

Run the Go 1.26 release gate: tidy/checksum verification, the complete
hermetic test suite, static Linux release builds, artifact smoke tests, and
optionally the production-container startup check.
EOF
}

repository_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
output_dir="$repository_root/out/release"
with_container=false

while [ "$#" -gt 0 ]; do
	case "$1" in
		--container) with_container=true; shift ;;
		--output)
			[ "$#" -ge 2 ] || { usage >&2; exit 2; }
			output_dir=$2
			shift 2
			;;
		-h|--help) usage; exit 0 ;;
		*) echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
	esac
done

case "$(go version)" in
	*" go1.26"*) ;;
	*) echo "Go 1.26 is required; found $(go version)" >&2; exit 1 ;;
esac

cd "$repository_root"
echo "==> Verifying module metadata"
go mod tidy -diff
go mod verify

echo "==> Running the complete hermetic test suite"
go test -timeout=15m ./...

echo "==> Building static release artifacts"
sh scripts/build-release.sh --output "$output_dir"

echo "==> Rebuilding to verify reproducibility"
reproducibility_dir=$(mktemp -d)
cleanup_reproducibility() {
	rm -rf "$reproducibility_dir"
}
trap cleanup_reproducibility EXIT HUP INT TERM
sh scripts/build-release.sh --output "$reproducibility_dir"
cmp "$output_dir/XrayR-linux-64.zip" "$reproducibility_dir/XrayR-linux-64.zip"
cmp "$output_dir/XrayR-linux-arm64-v8a.zip" "$reproducibility_dir/XrayR-linux-arm64-v8a.zip"
cleanup_reproducibility
trap - EXIT HUP INT TERM

echo "==> Smoke-testing release artifacts"
sh scripts/smoke-release.sh "$output_dir"

if [ "$with_container" = true ]; then
	echo "==> Building and smoke-testing the production container"
	sh scripts/smoke-container.sh
fi

echo "Release qualification passed"
