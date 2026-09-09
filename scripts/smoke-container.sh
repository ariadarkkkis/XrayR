#!/bin/sh
set -eu

usage() {
	cat <<'EOF'
Usage: scripts/smoke-container.sh [IMAGE_TAG]

Build and start the production image with a no-node configuration, then verify
the packaged binary, embedded Xray version, and GeoIP/GeoSite layout.
EOF
}

case "${1:-}" in
	-h|--help) usage; exit 0 ;;
esac

repository_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
image_tag=${1:-xrayr:release-candidate}
. "$repository_root/release/release.env"

work_dir=$(mktemp -d)
container_id=""
cleanup() {
	if [ -n "$container_id" ]; then
		docker rm -f "$container_id" >/dev/null 2>&1 || true
	fi
	rm -rf "$work_dir"
}
trap cleanup EXIT INT TERM

docker build --pull -t "$image_tag" "$repository_root"
container_id=$(docker run -d --volume "$repository_root/release/config/smoke-config.yml:/etc/XrayR/config.yml:ro" "$image_tag")

attempt=0
while [ "$attempt" -lt 10 ] && [ "$(docker inspect -f '{{.State.Running}}' "$container_id")" != "true" ]; do
	attempt=$((attempt + 1))
	sleep 1
done
[ "$(docker inspect -f '{{.State.Running}}' "$container_id")" = "true" ] || {
	docker logs "$container_id" >&2
	exit 1
}
docker cp "$container_id:/etc/XrayR/geoip.dat" "$work_dir/geoip.dat"
docker cp "$container_id:/etc/XrayR/geosite.dat" "$work_dir/geosite.dat"
test -s "$work_dir/geoip.dat"
test -s "$work_dir/geosite.dat"
docker exec "$container_id" /usr/local/bin/XrayR version | grep -F "XrayR $XRAYR_VERSION" >/dev/null
docker logs "$container_id" 2>&1 | grep -F "Xray Core Version: ${XRAY_CORE_RELEASE#v}" >/dev/null

echo "Production container smoke check passed"
