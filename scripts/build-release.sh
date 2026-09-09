#!/bin/sh
set -eu

usage() {
	cat <<'EOF'
Usage: scripts/build-release.sh [--output DIR] [--target OS/ARCH]

Build reproducible XrayR release archives. With no --target option, the
supported linux/amd64 and linux/arm64 artifacts are produced.
EOF
}

repository_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
output_dir="$repository_root/out/release"
targets=""

while [ "$#" -gt 0 ]; do
	case "$1" in
		--output)
			[ "$#" -ge 2 ] || { usage >&2; exit 2; }
			output_dir=$2
			shift 2
			;;
		--target)
			[ "$#" -ge 2 ] || { usage >&2; exit 2; }
			targets="$targets $2"
			shift 2
			;;
		-h|--help)
			usage
			exit 0
			;;
		*)
			echo "unknown argument: $1" >&2
			usage >&2
			exit 2
			;;
	esac
done

[ -n "$targets" ] || targets="linux/amd64 linux/arm64"
. "$repository_root/release/release.env"

case "$(go version)" in
	*" go1.26"*) ;;
	*) echo "Go 1.26 is required; found $(go version)" >&2; exit 1 ;;
esac

actual_xray_module=$(go list -m -f '{{.Version}}' github.com/xtls/xray-core)
[ "$actual_xray_module" = "$XRAY_CORE_MODULE_VERSION" ] || {
	echo "expected Xray module $XRAY_CORE_MODULE_VERSION, found $actual_xray_module" >&2
	exit 1
}

git -C "$repository_root" diff --quiet HEAD -- \
	':(glob)**/*.go' \
	go.mod go.sum README.md LICENSE \
	release/release.env 'release/config/**' || {
	echo "release inputs differ from the recorded source commit; commit them before building" >&2
	exit 1
}
release_changes=$(git -C "$repository_root" status --porcelain --untracked-files=all -- \
	':(glob)**/*.go' \
	go.mod go.sum README.md LICENSE \
	release/release.env 'release/config/**')
[ -z "$release_changes" ] || {
	printf '%s\n' "$release_changes" >&2
	echo "release inputs differ from the recorded source commit; commit them before building" >&2
	exit 1
}

source_date_epoch=${SOURCE_DATE_EPOCH:-$(git -C "$repository_root" log -1 --format=%ct)}
source_commit=$(git -C "$repository_root" rev-parse HEAD)
build_toolchain=$(go version | awk '{print $3}')
mkdir -p "$output_dir"

for target in $targets; do
	case "$target" in
		linux/amd64) asset_name=linux-64 ;;
		linux/arm64) asset_name=linux-arm64-v8a ;;
		*) echo "unsupported release target: $target" >&2; exit 2 ;;
	esac

	goos=${target%/*}
	goarch=${target#*/}
	staging_root=$(mktemp -d)
	cleanup_staging() {
		rm -rf "$staging_root"
	}
	trap cleanup_staging EXIT HUP INT TERM
	package_dir="$staging_root/XrayR-$asset_name"
	mkdir -p "$package_dir"

	echo "Building XrayR-$asset_name with $build_toolchain"
	(
		cd "$repository_root"
		CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
			go build -v -o "$package_dir/XrayR" -trimpath -ldflags "-s -w -buildid=" .
	)

	cp "$repository_root/README.md" "$package_dir/README.md"
	cp "$repository_root/LICENSE" "$package_dir/LICENSE"
	cp "$repository_root/release/config/dns.json" "$package_dir/dns.json"
	cp "$repository_root/release/config/route.json" "$package_dir/route.json"
	cp "$repository_root/release/config/custom_outbound.json" "$package_dir/custom_outbound.json"
	cp "$repository_root/release/config/custom_inbound.json" "$package_dir/custom_inbound.json"
	cp "$repository_root/release/config/rulelist" "$package_dir/rulelist"
	cp "$repository_root/release/config/config.yml.example" "$package_dir/config.yml"
	cp "$repository_root/release/config/geoip.dat" "$package_dir/geoip.dat"
	cp "$repository_root/release/config/geosite.dat" "$package_dir/geosite.dat"

	cat >"$package_dir/RELEASE-METADATA.txt" <<EOF
XrayR version: $XRAYR_VERSION
Embedded Xray release: $XRAY_CORE_RELEASE
Embedded Xray Go module: github.com/xtls/xray-core $XRAY_CORE_MODULE_VERSION
Go toolchain: $build_toolchain
Target: $target
CGO_ENABLED: 0
Source commit: $source_commit
Source date epoch: $source_date_epoch
EOF

	find "$package_dir" -exec touch -d "@$source_date_epoch" {} +
	archive="$output_dir/XrayR-$asset_name.zip"
	archive_tmp="$staging_root/XrayR-$asset_name.zip"
	(
		cd "$package_dir"
		find . -type f -printf '%P\n' | LC_ALL=C sort | zip -X -9 -q "$archive_tmp" -@
	)
	cp "$archive_tmp" "$archive"

	checksum_file="$archive.dgst"
	: >"$checksum_file"
	for method in md5 sha1 sha256 sha512; do
		openssl dgst "-$method" "$archive" | sed 's/([^)]*)//g' >>"$checksum_file"
	done
	cleanup_staging
	trap - EXIT HUP INT TERM
done

echo "Release artifacts written to $output_dir"
