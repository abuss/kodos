#!/bin/sh
# vm/guest-test/30-selfup.sh — GUEST side of the self-upgrade round (paste):
#   host:   bash vm/guest-test/selfup-build.sh      # newer kodos.apk into the guest repo
#   guest:  vm/run.sh guest "$(cat vm/guest-test/30-selfup.sh)"
# The machine upgrades its OWN manager: refresh the local repo index with the
# guest's real /sbin/apk, apply, and the new generation must carry the newer
# kodos while every older generation's binary stays intact on disk.
PATH=/sbin:/bin:/usr/sbin:/usr/bin:$PATH
G=/kod/generation
R=/var/kodos/repo/x86_64
fails=0
line() { echo "GUEST-TEST: $1 $2${3:+ $3}"; [ "$2" = FAIL ] && fails=$((fails + 1)); return 0; }
cur() { basename "$(readlink "$G/current" 2>/dev/null)"; }

# newest by mtime: the apk the host just copied in is the freshest file here
# (ls -t avoids busybox sort -V, which is not built into Alpine's busybox)
newest=$(ls -t "$R"/kodos-*.apk 2>/dev/null | head -1)
[ -n "$newest" ] || { echo "GUEST-TEST: repo-has-new-kodos FAIL (no kodos-*.apk in $R: run vm/guest-test/selfup-build.sh on the host first)"; exit 1; }
newver=$(basename "$newest" .apk); newver=${newver#kodos-}
echo "== guest repo: $(ls "$R" | tr '\n' ' ')"
line repo-has-new-kodos PASS "(newest: kodos-$newver)"

# index refresh is the guest's own job: /sbin/apk index over every apk in the
# repo dir (dummy + both kodos versions), same layout mkrepo/mkapk produce
if /sbin/apk index --allow-untrusted --no-warnings -o "$R/APKINDEX.tar.gz" "$R"/*.apk; then
    line index-refresh PASS
else
    line index-refresh FAIL
fi
if tar -xzOf "$R/APKINDEX.tar.gz" APKINDEX | grep -q "^V:$newver\$"; then
    line index-names-new PASS "(APKINDEX has V:$newver)"
else
    line index-names-new FAIL "(APKINDEX lacks V:$newver)"
    tar -xzOf "$R/APKINDEX.tar.gz" APKINDEX | grep -E '^(P|V):kodos|^V:' | head -8
fi

before=$(cur)
if kodos apply /etc/kodos > /tmp/gt-selfup.log 2>&1; then
    line apply PASS
    grep -E 'install_packages:|generation [0-9]+ created' /tmp/gt-selfup.log
else
    line apply FAIL; tail -25 /tmp/gt-selfup.log
fi
after=$(cur)
echo "current: $before -> $after"

if grep -q "\"kodos\":{\"version\":\"$newver\"" "$G/current/generation.json"; then
    line kodos-upgraded PASS "(generation $after pins kodos $newver)"
else
    line kodos-upgraded FAIL "(generation $after pins: $(grep -o '"kodos":{[^}]*}' "$G/current/generation.json"))"
fi

store=$(ls /kod/store/kodos 2>/dev/null | tr '\n' ' ')
case " $store " in
    *" $newver "*) line store-both PASS "(store/kodos: $store)";;
    *) line store-both FAIL "(store/kodos lacks $newver: $store)";;
esac
# older generations keep their own binaries (rollback stays possible), and the
# kodos answering right now is the one the NEW generation ships
if [ -x "$G/$before/usr/bin/kodos" ] && ls -d /kod/store/kodos/*/ >/dev/null 2>&1; then
    line old-binary-intact PASS "($G/$before/usr/bin/kodos still there; live $(readlink -f /usr/bin/kodos))"
else
    line old-binary-intact FAIL "(generation $before's binary is gone)"
fi
if kodos selftest > /tmp/gt-selfup-self.log 2>&1; then
    line new-binary-runs PASS "(the kodos of generation $after passes its own selftest)"
else
    line new-binary-runs FAIL; tail -10 /tmp/gt-selfup-self.log
fi

echo "GUEST-TEST: 30-selfup done (current=$after pins kodos $newver, failures=$fails)"
exit $fails
