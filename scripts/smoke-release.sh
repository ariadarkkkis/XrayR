#!/bin/sh
set -eu

usage() {
	cat <<'EOF'
Usage: scripts/smoke-release.sh [DIST_DIR]

Validate release archive contents, target architecture, version output, root
help, relied-upon embedded commands, and the no-node configuration startup.
EOF
}

case "${1:-}" in
	-h|--help) usage; exit 0 ;;
esac

repository_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
dist_dir=${1:-"$repository_root/out/release"}
. "$repository_root/release/release.env"

smoke_archive() {
	archive=$1
	expected_arch=$2
	work_dir=$(mktemp -d)
	unzip -q "$archive" -d "$work_dir"
	binary="$work_dir/XrayR"

	test -x "$binary"
	grep -F "XrayR version: $XRAYR_VERSION" "$work_dir/RELEASE-METADATA.txt" >/dev/null
	grep -F "Embedded Xray release: $XRAY_CORE_RELEASE" "$work_dir/RELEASE-METADATA.txt" >/dev/null
	grep -F "Embedded Xray Go module: github.com/xtls/xray-core $XRAY_CORE_MODULE_VERSION" "$work_dir/RELEASE-METADATA.txt" >/dev/null
	file "$binary" | grep -F "$expected_arch" >/dev/null
	if ldd "$binary" >/dev/null 2>&1; then
		echo "$archive is dynamically linked" >&2
		exit 1
	fi

	case "$expected_arch/$(uname -m)" in
		*x86-64*/x86_64|*x86-64*/amd64)
			version_output=$("$binary" version)
			printf '%s\n' "$version_output" | grep -F "XrayR $XRAYR_VERSION" >/dev/null
			"$binary" help 2>&1 | grep -F "XrayR" >/dev/null
			"$binary" help uuid 2>&1 | grep -i "uuid" >/dev/null
			"$binary" help x25519 2>&1 | grep -i "x25519" >/dev/null
			"$binary" uuid | grep -E '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$' >/dev/null
			x25519_output=$("$binary" x25519)
			printf '%s\n' "$x25519_output" | grep -F "PrivateKey:" >/dev/null
			printf '%s\n' "$x25519_output" | grep -F "Password (PublicKey):" >/dev/null
			printf '%s\n' "$x25519_output" | grep -F "Hash32:" >/dev/null

			set +e
			timeout --signal=TERM 3 "$binary" --config "$repository_root/release/config/smoke-config.yml" >"$work_dir/startup.log" 2>&1
			startup_status=$?
			set -e
			[ "$startup_status" -eq 124 ] || {
				cat "$work_dir/startup.log" >&2
				echo "XrayR exited before the startup observation window (status $startup_status)" >&2
				exit 1
			}
			grep -F "Xray Core Version: ${XRAY_CORE_RELEASE#v}" "$work_dir/startup.log" >/dev/null
			;;
	esac

	rm -rf "$work_dir"
}

smoke_archive "$dist_dir/XrayR-linux-64.zip" "x86-64"
smoke_archive "$dist_dir/XrayR-linux-arm64-v8a.zip" "ARM aarch64"
echo "Release artifact smoke checks passed"
