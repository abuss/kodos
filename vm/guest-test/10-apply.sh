#!/bin/sh
# vm/guest-test/10-apply.sh — the machine reconfigures ITSELF (paste round):
#   vm/run.sh guest "$(cat vm/guest-test/10-apply.sh)"
# /etc/kodos/config.lua (shipped by the bootstrap) declares the fixture
# package `dummy`, which the bootstrap generation does NOT carry, so this
# apply is a live package ADD on a running machine. Expect: no storage ops
# (the --inplace stream skips partition/format), a new generation that is now
# current, and `dummy` pinned inside it.
PATH=/sbin:/bin:/usr/sbin:/usr/bin:$PATH
G=/kod/generation
fails=0
# line <case> <PASS|FAIL> [detail]
line() { echo "GUEST-TEST: $1 $2${3:+ $3}"; [ "$2" = FAIL ] && fails=$((fails + 1)); return 0; }
gens() { ls "$G" | grep -c '^[0-9]'; }
cur() { basename "$(readlink "$G/current" 2>/dev/null)"; }

before=$(gens); curbefore=$(cur)
echo "== before: generations=$before current=$curbefore"

if kodos apply /etc/kodos --dry-run > /tmp/gt-apply-dry.log 2>&1; then
    line apply-dry PASS
    cat /tmp/gt-apply-dry.log
else
    line apply-dry FAIL; tail -20 /tmp/gt-apply-dry.log
fi
if grep -q dummy /tmp/gt-apply-dry.log; then
    line dry-names-dummy PASS "(the plan already shows it)"
else
    line dry-names-dummy FAIL "(dry-run plan does not name dummy)"
fi

# the real round takes minutes: full closure, activation, hooks, mkinitfs, grub
if kodos apply /etc/kodos > /tmp/gt-apply.log 2>&1; then
    line apply PASS
    grep -E 'install_packages:|generation [0-9]+ created' /tmp/gt-apply.log
else
    line apply FAIL; tail -25 /tmp/gt-apply.log
fi

if kodos status > /tmp/gt-status.log 2>&1; then line status PASS; else line status FAIL; fi
cat /tmp/gt-status.log

after=$(gens); curafter=$(cur)
if [ "$after" -gt "$before" ] && [ "$curafter" != "$curbefore" ]; then
    line gen-increment PASS "(gens $before -> $after, current $curbefore -> $curafter)"
else
    line gen-increment FAIL "(gens $before -> $after, current $curbefore -> $curafter)"
fi

if grep -q '"dummy":' "$G/current/generation.json" && ls -d /kod/store/dummy/*/ >/dev/null 2>&1; then
    line dummy-live-add PASS "($(grep -o '"dummy":{[^}]*}' "$G/current/generation.json"), store: $(ls /kod/store/dummy | tr '\n' ' '))"
else
    line dummy-live-add FAIL "(dummy missing from the current manifest or the store)"
fi

if grep -qE '"op": "(partition_disk|format|create_swap)"' "/kod/actions/actions-$curafter.json"; then
    line no-storage-ops FAIL "(storage ops in the staged stream — --inplace lost?)"
else
    line no-storage-ops PASS "(staged actions-$curafter.json has no partition/format/swap ops)"
fi

echo "GUEST-TEST: 10-apply done (current=$curafter, generations=$after, failures=$fails)"
exit $fails
