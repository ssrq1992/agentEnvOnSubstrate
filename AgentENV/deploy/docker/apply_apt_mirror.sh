#!/usr/bin/env sh
set -eu

mirror_base="${1:-}"
if [ -z "${mirror_base}" ]; then
    exit 0
fi

mirror_base="${mirror_base%/}"

if [ -r /etc/os-release ]; then
    # shellcheck disable=SC1091
    . /etc/os-release
else
    echo "cannot detect distro for apt mirror rewrite: /etc/os-release missing" >&2
    exit 1
fi

rewrite_sources() {
    distro_path="$1"
    security_path="$2"
    shift 2
    for source_file in /etc/apt/sources.list /etc/apt/sources.list.d/*.sources; do
        [ -f "${source_file}" ] || continue
        sed -i \
            -e "s|http://deb.debian.org/debian-security|${mirror_base}/${security_path}|g" \
            -e "s|http://deb.debian.org/debian|${mirror_base}/${distro_path}|g" \
            -e "s|http://archive.ubuntu.com/ubuntu|${mirror_base}/${distro_path}|g" \
            -e "s|http://security.ubuntu.com/ubuntu|${mirror_base}/${security_path}|g" \
            "${source_file}"
    done
}

case "${ID:-}" in
    debian)
        rewrite_sources debian debian-security
        ;;
    ubuntu)
        # Ubuntu archive mirrors usually expose both archive and security suites
        # under the same /ubuntu path.
        rewrite_sources ubuntu ubuntu
        ;;
    *)
        echo "unsupported apt mirror distro: ${ID:-unknown}" >&2
        exit 1
        ;;
esac
