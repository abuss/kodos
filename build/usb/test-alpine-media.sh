#!/bin/sh
# test-alpine-media.sh — host-side checks for the Alpine install media payload.
# The Alpine media must deliver kodos as a static binary in generation 0, with
# no kodos.apk and no local repo declared just to carry it.
set -eu

REPO=$(cd "$(dirname "$0")/../.." && pwd)
TDIR=${TMPDIR:-/tmp/opencode}
STAGE=$TDIR/alpine-stage-test
IR=$TDIR/alpine-media-ir.json
mkdir -p "$TDIR"

sh -n "$REPO/build/usb/mkinstall-usb.sh" || {
    echo "FAIL: staging script syntax"; exit 1;
}
sh -n "$REPO/build/usb/run-install.sh" || {
    echo "FAIL: installer script syntax"; exit 1;
}

STAGE_ONLY=1 OUT="$STAGE" sh "$REPO/build/usb/mkinstall-usb.sh" \
    > "$TDIR/alpine-stage.log" 2>&1 || {
    tail -20 "$TDIR/alpine-stage.log"; echo "FAIL: staging"; exit 1;
}

for f in bin/kodos indexes/main-324.apkindex indexes/community-324.apkindex \
         config/machine.lua config/target-config.lua repo/x86_64/APKINDEX.tar.gz; do
    [ -s "$STAGE/$f" ] || { echo "FAIL: missing staged $f"; exit 1; }
done

if ls "$STAGE"/repo/x86_64/kodos-*.apk >/dev/null 2>&1; then
    echo "FAIL: staged payload still ships kodos.apk"; exit 1
fi

# Neither the bootstrap layer nor the machine's own day-2 config may point at
# a local repo for kodos (copy_tree + file:// url + package entry).
for f in machine.lua target-config.lua; do
    if grep -Fq 'file:///kod/repo' "$STAGE/config/$f"; then
        echo "FAIL: $f still declares a local kodos repo"; exit 1
    fi
    if grep -Fq 'name = "kodos"' "$STAGE/config/$f"; then
        echo "FAIL: $f still requests the kodos package"; exit 1
    fi
done

# Real IR from the staged config: no copy_tree to /kod/repo, no local repo in
# configure_repos, no kodos in the install closure.
"$STAGE/bin/kodos" lua generate.lua system.lua "$STAGE/config/machine.lua" > "$IR"
if jq -e '.actions[] | select(.op=="copy_tree" and .dst=="/kod/repo")' "$IR" >/dev/null; then
    echo "FAIL: IR still copies a local repo to /kod/repo"; exit 1
fi
if jq -e '.actions[] | select(.op=="configure_repos") | (.repos // [])[] | select(.name=="kodos")' "$IR" >/dev/null; then
    echo "FAIL: IR still configures the kodos repo"; exit 1
fi
if jq -e '.actions[] | select(.op=="install_packages") | (.pkgs // [])[] | select(.=="kodos")' "$IR" >/dev/null; then
    echo "FAIL: IR still installs kodos from an apk"; exit 1
fi

# The installer must place the static binary in the real generation directory:
# after activate_generation, $TARGET_ROOT/usr is a chroot-rooted symlink.
grep -Fq 'GEN="$TARGET_ROOT/kod/generation/0"' "$REPO/build/usb/run-install.sh" || {
    echo "FAIL: installer has no generation-0 destination"; exit 1;
}
grep -Fq '"$GEN/usr/bin/kodos"' "$REPO/build/usb/run-install.sh" || {
    echo "FAIL: installer does not install static kodos into generation 0"; exit 1;
}
if grep -Fq '"$TARGET_ROOT/usr/bin/kodos"' "$REPO/build/usb/run-install.sh"; then
    echo "FAIL: installer writes static kodos through the activated /usr symlink"; exit 1
fi
grep -Fq 'lockfile.lua 0 "$IM" "$IC" --' "$REPO/build/usb/run-install.sh" || {
    echo "FAIL: installer still passes a local repo index to the closure lock"; exit 1;
}

# --force-wipe: the installer must inject the wipe field (generated streams
# never carry it) and confirm the wipe by typed device path, not "yes".
grep -Fq '"op": "partition_disk", "wipe": true' "$REPO/build/usb/run-install.sh" || {
    echo "FAIL: installer does not inject the wipe field on --force-wipe"; exit 1;
}
grep -Fq 'type the device path' "$REPO/build/usb/run-install.sh" || {
    echo "FAIL: installer has no device-path wipe confirmation"; exit 1;
}

# Dry-run the real installer: no local-repo ops may survive into the stream.
DRY=$(KODOS_APK="$REPO/vm/apk-tools-static" "$STAGE/run-install.sh" \
    --dry-run --device /dev/nbd99 2>&1) || {
    printf '%s\n' "$DRY" | tail -20; echo "FAIL: installer dry-run"; exit 1;
}
printf '%s\n' "$DRY" | grep -q 'dry-run done' || {
    echo "FAIL: installer did not finish its dry-run"; exit 1;
}
if printf '%s\n' "$DRY" | grep -qE 'APKINDEX|file:///|/kod/repo|DRY-RUN copy_tree'; then
    echo "FAIL: dry-run leaked local-repo ops"; exit 1
fi

# --force-wipe must reach the planned stream; the default plan must never wipe.
FORCE=$(KODOS_APK="$REPO/vm/apk-tools-static" "$STAGE/run-install.sh" \
    --dry-run --force-wipe --device /dev/nbd99 2>&1)
if ! printf '%s\n' "$FORCE" | grep -q 'DRY-RUN partition_disk.*force-wipe'; then
    printf '%s\n' "$FORCE" | tail -15
    echo "FAIL: --force-wipe never reaches the planned stream"; exit 1
fi
if printf '%s\n' "$DRY" | grep -q 'force-wipe'; then
    echo "FAIL: the default install plan wipes a partition table"; exit 1
fi

# Docs must not advertise the repo-based self-update any more.
if grep -Fq 'kodos self-update' "$STAGE/README.md"; then
    echo "FAIL: media docs still advertise kodos self-update"; exit 1
fi

echo "test-alpine-media installer: ok"
