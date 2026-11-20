#!/bin/sh
# build/usb/mkinstall-arch-iso.sh — Arch-flavoured kodos install ISO.
#
#   sh build/usb/mkinstall-arch-iso.sh [out.iso]
#     -> build/usb/iso/kodos-arch-install-<rel>.iso
#
# Thin wrapper: the shared xorriso remaster stays byte-identical; only the
# staging script is swapped. The booted live host remains Alpine 3.24.
set -eu

REPO=$(cd "$(dirname "$0")/../.." && pwd)
REL=${ALPINE_REL:-3.24.4}
STAGE=${STAGE:-$REPO/build/usb/arch-stage}
OUT=${1:-$REPO/build/usb/iso/kodos-arch-install-$REL.iso}

export STAGE STAGE_SCRIPT="$REPO/build/usb/mkinstall-arch-usb.sh"
exec sh "$REPO/build/usb/mkinstall-iso.sh" "$OUT"
