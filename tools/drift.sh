#!/bin/sh
# drift.sh — weekly drift check: re-fetch real distro indexes, then run
# every test suite against them. The failure mode this guards (proven by
# clang19->clang20): backends/mappings naming packages that stopped
# existing. Exits nonzero on any failure.
#
# Index snapshots land under /tmp/opencode (same paths test-lock.lua expects).
set -u
D=/tmp/opencode
mkdir -p "$D"
fails=0
step() { # name, then command
    name=$1; shift
    if "$@" >/dev/null 2>&1; then echo "ok    $name"
    else echo "FAIL  $name"; fails=$((fails+1)); fi
}

echo "== fetch indexes"
for r in main community; do
    step "fetch alpine v3.24 $r" sh -c \
        "curl -sf 'https://dl-cdn.alpinelinux.org/alpine/v3.24/$r/x86_64/APKINDEX.tar.gz' \
         | tar -xzO APKINDEX > $D/$r-324.apkindex"
    step "derive alpine $r pkg list" sh -c \
        "grep '^P:' $D/$r-324.apkindex | cut -d: -f2- | sort -u > $D/$r-324.pkgs"
done
# arch .db files are zstd tars; %NAME% line is followed by the name.
# community.db may be gone (merged into extra) -> optional.
step "fetch arch indexes" sh -c "
    got=''
    for repo in core extra community; do
        if curl -sf 'https://geo.mirror.pkgbuild.com/'\$repo'/os/x86_64/'\$repo'.db' \
           | tar -xO --zstd 2>/dev/null | sed -n '/^%NAME%$/{n;p}' > $D/arch-\$repo.txt; \
           [ -s $D/arch-\$repo.txt ]; then got=yes; fi
    done
    [ -n \"\$got\" ] && cat $D/arch-*.txt | sort -u > $D/arch.pkgs"
# debian trixie: main + contrib + non-free-firmware (real installs enable all
# three; firmware-linux etc. live in non-free-firmware). The backend's
# drop-list carries what none of them has.
step "fetch deb trixie index" sh -c "
    for c in main contrib non-free-firmware; do
        curl -sf 'http://deb.debian.org/debian/dists/trixie/'\$c'/binary-amd64/Packages.xz' \
            | xz -dc | sed -n 's/^Package: //p'
    done | sort -u > $D/deb.all"

E=$(dirname "$0")/../example/eszkoz
X=$(dirname "$0")/../executor

echo "== lua suites"
for t in test-equivalence test-merge test-validate test-generate test-lock \
         check-test-arch check-test-deb; do
    case $t in
        check-test-arch) step "checker arch" sh -c \
            "cd $E && lua check-packages.lua $D/arch.pkgs $D/arch.pkgs \
             system.lua layers/ci-arch.lua layers/machine.lua layers/user.lua | tail -1 | grep -q '0 missing'" ;;
        check-test-deb) step "checker deb" sh -c \
            "cd $E && lua check-packages.lua $D/deb.all $D/deb.all \
             system.lua layers/ci-deb.lua layers/machine.lua layers/user.lua | tail -1 | grep -q '0 missing'" ;;
        *) step "$t" sh -c "cd $E && lua $t.lua | tail -1 | grep -q '^PASS'" ;;
    esac
done
step "checker alpine" sh -c \
    "cd $E && lua check-packages.lua $D/main-324.pkgs $D/community-324.pkgs \
     system.lua layers/machine.lua layers/user.lua | tail -1 | grep -q '0 missing'"

echo "== executor"
step "go vet" sh -c "cd $X && go vet ./..."
step "go build" sh -c "cd $X && go build -o kodos-exec ."
step "executor selftest" sh -c "cd $X && ./kodos-exec selftest | tail -1 | grep -q '^PASS'"

echo
if [ $fails -eq 0 ]; then echo "DRIFT: all green"; else echo "DRIFT: $fails step(s) FAILED"; exit 1; fi
