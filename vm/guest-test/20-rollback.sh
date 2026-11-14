#!/bin/sh
# vm/guest-test/20-rollback.sh — the machine rolls itself back (paste round):
#   vm/run.sh guest "$(cat vm/guest-test/20-rollback.sh)"
# `kodos rollback` with no N re-activates the highest generation strictly
# below current, through the SAME executor path an apply uses (activate +
# bootloader). The generation table must keep every generation on disk;
# rebooting after this lands on the older one (NEXT.md checks that).
PATH=/sbin:/bin:/usr/sbin:/usr/bin:$PATH
G=/kod/generation
fails=0
line() { echo "GUEST-TEST: $1 $2${3:+ $3}"; [ "$2" = FAIL ] && fails=$((fails + 1)); return 0; }
gens() { ls "$G" | grep -c '^[0-9]'; }
cur() { basename "$(readlink "$G/current" 2>/dev/null)"; }

before=$(gens); curbefore=$(cur)
echo "== before: generations=$before current=$curbefore"
kodos status

if kodos rollback > /tmp/gt-rollback.log 2>&1; then
    line rollback PASS
    grep -E 'rollback:|reboot to boot' /tmp/gt-rollback.log
else
    line rollback FAIL; tail -25 /tmp/gt-rollback.log
fi

kodos status

after=$(gens); curafter=$(cur)
if [ "$curafter" -lt "$curbefore" ] 2>/dev/null; then
    line current-dropped PASS "(current $curbefore -> $curafter)"
else
    line current-dropped FAIL "(current $curbefore -> $curafter)"
fi
if [ "$after" -ge "$before" ] 2>/dev/null; then
    line gens-intact PASS "(still $after generation dirs: $(ls "$G" | tr '\n' ' '))"
else
    line gens-intact FAIL "(generation dirs vanished: $before -> $after)"
fi

# the target must be a REAL bootable tree, and the new default in grub
if grep -qE "menuentry '?Kodos generation $curafter'?" /boot/grub/grub.cfg; then
    line grub-default PASS "(grub.cfg entry for generation $curafter)"
else
    line grub-default FAIL "(no grub entry naming generation $curafter)"
    grep -m2 menuentry /boot/grub/grub.cfg
fi
if ls "$G/$curafter"/boot/vmlinuz-* "$G/$curafter"/boot/initramfs-* >/dev/null 2>&1; then
    line target-kernel PASS "($(ls "$G/$curafter"/boot/vmlinuz-* | tr '\n' ' '))"
else
    line target-kernel FAIL "(no kernel/initramfs in $G/$curafter/boot)"
fi
# the rollback re-activates an older manifest: the live-add it does not know
# about must not be in the target generation's package set
if grep -q '"dummy":' "$G/current/generation.json"; then
    line dummy-not-in-target FAIL "(generation $curafter still names dummy)"
else
    line dummy-not-in-target PASS "(generation $curafter predates the dummy add)"
fi

echo "GUEST-TEST: 20-rollback done (current=$curafter; reboot lands there)"
exit $fails
