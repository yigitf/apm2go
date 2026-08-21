#!/usr/bin/env bash
#
# Builds apm2go for Linux and produces RPM and DEB packages.
#
# Everything runs in containers: the Go build needs Linux because the DuckDB
# driver uses cgo, and nfpm is containerised so no distribution packaging tools
# have to be installed on the build host.
set -euo pipefail

cd "$(dirname "$0")/.."

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo 0.1.0)}"
COMMIT="${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo none)}"
BUILD_DATE="${BUILD_DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
ARCHES="${ARCHES:-amd64 arm64}"

# nfpm rejects a leading "v" and anything that is not semver-ish, so a git
# describe such as "v0.2.0-3-gabc1234" is normalised here rather than failing
# minutes into the build.
PKG_VERSION="$(echo "${VERSION#v}" | sed 's/-dirty$//; s/-g[0-9a-f]*$//; s/-/./g')"

OBI_VERSION="${OBI_VERSION:-0.11.0}"
EBPF_DIR="internal/ebpf/files"

mkdir -p dist "${EBPF_DIR}"

echo "==> Building apm2go ${VERSION} for: ${ARCHES}"
for arch in ${ARCHES}; do
    echo "--> linux/${arch}"

    # OBI is architecture-specific, unlike the Java agent jars, so it is fetched
    # fresh for each arch immediately before that arch's build — overwriting
    # whatever the previous iteration left here.
    echo "    fetching OBI v${OBI_VERSION} (${arch})"
    curl -sSLf -o /tmp/obi.tar.gz \
        "https://github.com/open-telemetry/opentelemetry-ebpf-instrumentation/releases/download/v${OBI_VERSION}/obi-v${OBI_VERSION}-linux-${arch}.tar.gz"
    tar -xzf /tmp/obi.tar.gz -O obi > "${EBPF_DIR}/obi"
    chmod +x "${EBPF_DIR}/obi"
    rm -f /tmp/obi.tar.gz

    docker build \
        --platform "linux/${arch}" \
        -f build/Dockerfile.build \
        --target builder \
        --build-arg "VERSION=${VERSION}" \
        --build-arg "COMMIT=${COMMIT}" \
        --build-arg "BUILD_DATE=${BUILD_DATE}" \
        -t "apm2go-builder:${arch}" \
        . >/dev/null

    container="$(docker create --platform "linux/${arch}" "apm2go-builder:${arch}")"
    docker cp "${container}:/out/apm2go" "dist/apm2go_${arch}"
    docker rm "${container}" >/dev/null
    echo "    dist/apm2go_${arch} ($(du -h "dist/apm2go_${arch}" | cut -f1))"
done

echo "==> Packaging ${PKG_VERSION}"
for arch in ${ARCHES}; do
    # The placeholders are substituted here rather than left to nfpm: its own
    # environment expansion does not reach the contents[].src field, which
    # fails late with an unhelpful glob error.
    sed -e "s|\${ARCH}|${arch}|g" -e "s|\${VERSION}|${PKG_VERSION}|g" \
        packaging/nfpm.yaml > "dist/nfpm_${arch}.yaml"

    for format in rpm deb; do
        docker run --rm \
            -v "$PWD:/work" -w /work \
            goreleaser/nfpm:latest package \
            --config "dist/nfpm_${arch}.yaml" \
            --packager "${format}" \
            --target /work/dist/ >/dev/null
    done
    rm -f "dist/nfpm_${arch}.yaml"
done

echo
echo "==> Artifacts"
ls -1sh dist/
