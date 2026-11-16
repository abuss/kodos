#!/bin/sh
# vm/guest-test/40-tools-gone.sh — read-only forensics for the round where
# `kodos apply` destroyed live tooling mid-run (cat/ls/tail not found).
# BUILTINS ONLY: no ls/cat/tail/grep/readlink/basename. kodos itself is static
# Go and still runs. Run: vm/run.sh guest "$(cat vm/guest-test/40-tools-gone.sh)"
PATH=/sbin:/bin:/usr/sbin:/usr/bin:$PATH

echo "== kodos status"
kodos status 2>&1 || echo "status exit $?"

echo "== live paths (existence follows symlinks: broken link => NO)"
for p in /bin/sh /bin/ls /bin/cat /bin/tail /bin/busybox /bin/readlink \
         /sbin/init /usr/bin/kodos \
         /kod/generation/current/bin/ls /kod/generation/current/sbin/init; do
    [ -e "$p" ] && echo "yes $p" || echo "NO  $p"
done

echo "== generations on disk (entry counts via glob+set: builtin-only census)"
for d in /kod/generation/*; do
    case $d in
        */current)
            echo "$d (alias) bin/ls resolves: $( [ -e $d/bin/ls ] && echo yes || echo NO )"
            ;;
        *)
            set -- $d/bin/*;     b=$#
            set -- $d/sbin/*;    s=$#
            set -- $d/usr/bin/*; u=$#
            echo "$d: bin=$b sbin=$s usrbin=$u"
            for f in sh ls cat busybox; do
                [ -e "$d/bin/$f" ] && echo "   bin/$f yes" || echo "   bin/$f NO"
            done
            [ -f "$d/generation.json" ] && echo "   generation.json yes" || echo "   generation.json NO"
            for f in $d/tmp/.kodos-hook-*; do [ -e "$f" ] && echo "   leftover hook script: $f"; done
            ;;
    esac
done

echo "== leaked gen-chroot mounts (anything under /kod in /proc/mounts)"
leak=0
while IFS= read l; do
    case $l in
        */kod*) echo "$l"; leak=1;;
    esac
done < /proc/mounts
[ $leak -eq 0 ] && echo "(none)"

echo "== apply log census: $([ -f /kod/gt-apply.log ] && echo present || echo MISSING)"
L=/kod/gt-apply.log
if [ -f "$L" ]; then
    n=0; fe=0
    while IFS= read l; do
        n=$((n+1))
        case $l in
            *ERROR*) [ $fe -eq 0 ] && fe=$n;;
        esac
    done < "$L"
    echo "lines=$n firstERRORline=$fe"
    echo "-- progress markers (op headers, created, switch, activate) --"
    n=0
    while IFS= read l; do
        n=$((n+1))
        case $l in
            "=="*|*"created ("*) echo "L$n: $l";;
        esac
    done < "$L"
    if [ $fe -gt 0 ]; then
        echo "-- from first ERROR +40 --"
        n=0
        while IFS= read l; do
            n=$((n+1))
            if [ $n -ge $fe ] && [ $n -le $((fe + 40)) ]; then echo "$l"; fi
        done < "$L"
    fi
    echo "-- last 15 lines --"
    start=1; [ $n -gt 15 ] && start=$((n - 14))
    n=0
    while IFS= read l; do
        n=$((n+1))
        [ $n -ge $start ] && echo "$l"
    done < "$L"
fi

echo "== staged actions"
for f in /kod/actions/*; do [ -e "$f" ] && echo "$f"; done

echo "== gt-status.log"
if [ -f /kod/gt-status.log ]; then
    while IFS= read l; do echo "$l"; done < /kod/gt-status.log
fi
echo "== 40-tools-gone done"
