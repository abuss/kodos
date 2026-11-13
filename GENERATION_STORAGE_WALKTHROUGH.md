# Generation Storage: Visual Walkthrough

This document shows **exactly what exists on disk** at each step of the generation-apks process.

---

## Initial State (Before Any Installation)

```
/home/user/kodos-work/rootfs/
└── kod/
    ├── cache/            ← Downloaded .apk files go here
    ├── store/            ← Extracted package contents
    └── generation/       ← Generation directories
```

Empty. Nothing exists yet.

---

## After Phase 1: Fresh Install with Alpine Base

**What user asked for**: `packages = ["alpine-base"]`

**Command run**:
```python
apk add --simulate alpine-base
# Returns: bash, musl, busybox, alpine-base, alpine-keys, alpine-mirrors, alpine-conf, ...
# (alpine-base pulls many dependencies)
```

**After download_unpack():**

```
/rootfs/kod/
├── cache/
│   ├── alpine-base-3.24.0-r0.apk      ← Downloaded
│   ├── bash-5.3.9-r1.apk              ← Downloaded
│   ├── busybox-1.37.0-r0.apk          ← Downloaded
│   ├── musl-1.2.5-r0.apk              ← Downloaded
│   ├── alpine-keys-2.4-r1.apk         ← Downloaded
│   └── ... (more meta packages)
│
└── store/                             ← Extracted packages
    ├── alpine-base/3.24.0-r0/         ← Just a marker (virtual package)
    ├── bash/
    │   └── 5.3.9-r1/
    │       ├── bin/
    │       │   ├── bash
    │       │   ├── sh -> busybox      ← Link to other package!
    │       │   └── ...
    │       ├── etc/
    │       │   └── shells
    │       ├── usr/
    │       │   ├── bin/
    │       │   └── share/
    │       └── var/
    │
    ├── busybox/
    │   └── 1.37.0-r0/
    │       ├── bin/
    │       │   ├── busybox            ← Real binary
    │       │   ├── sh -> busybox      ← Symlink to self
    │       │   └── ...
    │       ├── etc/
    │       └── var/
    │
    ├── musl/
    │   └── 1.2.5-r0/
    │       └── lib/
    │           ├── ld-musl-x86_64.so.1
    │           └── libc.so.6 -> ld-musl-x86_64.so.1
    │
    └── ... (more packages)
```

**After create_generation(0, to_install):**

```
/rootfs/kod/generation/
└── 0/
    ├── bin -> /kod/store/bash/5.3.9-r1/bin           ← SYMLINK
    ├── usr -> /kod/store/bash/5.3.9-r1/usr           ← SYMLINK
    ├── lib -> /kod/store/musl/1.2.5-r0/lib           ← SYMLINK
    ├── var -> /kod/store/busybox/1.37.0-r0/var       ← SYMLINK
    ├── boot -> /kod/store/linux-lts/6.8.0-r0/boot    ← SYMLINK (kernel package)
    │
    ├── etc/                                           ← Real directory (mutable!)
    │   ├── hostname
    │   ├── fstab
    │   ├── shells
    │   ├── apk/
    │   ├── ssh/
    │   └── ...
    │
    ├── etc_default/                                   ← Pristine copies
    │   ├── hostname                (from alpine-base)
    │   ├── fstab                   (from alpine-base)
    │   ├── shells                  (from bash)
    │   └── ssh/
    │       └── sshd_config.example (from openssh, if installed)
    │
    └── etc_store/                                     ← Working copy (editable)
        ├── hostname                (copy of etc_default/hostname)
        ├── fstab
        ├── shells                  (from bash, untouched)
        └── ssh/
            └── sshd_config.example
```

**After activate_generation(0):**

Live filesystem changes:
```
/
├── bin -> /kod/generation/0/bin                      ← SYMLINK (points to store)
├── usr -> /kod/generation/0/usr                      ← SYMLINK (points to store)
├── lib -> /kod/generation/0/lib                      ← SYMLINK (points to store)
├── etc                                                ← Real directory (merged)
│   ├── hostname                   (from etc_store)
│   ├── shells                     (from etc_store)
│   └── ...
├── kod/
│   ├── store/                     ← All packages
│   └── generation/
│       ├── 0/                     ← Active generation
│       │   ├── bin -> store/bash/...
│       │   ├── etc/
│       │   ├── etc_default/
│       │   └── etc_store/
│       │
│       └── current -> /kod/generation/0   ← SYMLINK (atomic pointer)
```

**Storage on disk so far:**
```
Store (read-only): ~300 MB (bash, musl, busybox, linux, etc.)
Generation 0:      ~2 MB   (symlinks + etc copies)
Total:             ~302 MB
```

---

## After Phase 2: Rebuild with Added Packages

**User edits config**: `packages = ["alpine-base", "bash", "openssh", "zsh"]`

**What user hand-edited in Gen 0**:
- Modified `/etc/hostname` from "localhost" to "myhost"
- No other changes

**Command run**:
```python
apk add --simulate alpine-base bash openssh zsh
# Returns: bash, musl, busybox, openssh, zsh, ... (+ old packages)
```

**After download_unpack():**

```
/rootfs/kod/store/
├── bash/5.3.9-r1/               ← Already exists, skip
├── busybox/1.37.0-r0/           ← Already exists, skip
├── musl/1.2.5-r0/               ← Already exists, skip
├── openssh/
│   └── 8.9p1-r2/                ← NEW, extract
│       ├── bin/
│       │   └── ssh, sshd
│       ├── etc/
│       │   └── ssh/
│       │       ├── sshd_config
│       │       ├── ssh_config
│       │       └── ...
│       └── usr/
│
└── zsh/
    └── 5.9-r3/                  ← NEW, extract
        ├── bin/
        │   └── zsh
        ├── etc/
        │   └── zsh/
        │       └── zshrc
        └── usr/
```

**After create_generation(1, to_install, prev_generation=0):**

```
/rootfs/kod/generation/
├── 0/
│   ├── bin -> /kod/store/bash/5.3.9-r1/bin
│   ├── usr -> /kod/store/bash/5.3.9-r1/usr
│   ├── etc/
│   ├── etc_default/
│   └── etc_store/
│       └── hostname = "myhost"   ← User's edit
│
└── 1/
    ├── bin -> /kod/store/bash/5.3.9-r1/bin          ← Reused!
    ├── usr -> /kod/store/bash/5.3.9-r1/usr          ← Reused!
    ├── lib -> /kod/store/musl/1.2.5-r0/lib          ← Reused!
    ├── var -> /kod/store/busybox/1.37.0-r0/var      ← Reused!
    ├── boot -> /kod/store/linux-lts/6.8.0-r0/boot   ← Reused!
    │
    ├── bin                      ← NEW: merged from zsh!
    │   └── [conflicts - multiple packages want /bin]
    │       → generation/1/bin links to bash bin (first wins in store)
    │       → BUT zsh also provides /bin/zsh (how?)
    │          → This is simplified; really there's a bin tree that
    │            symlinks individual executables to right packages
    │
    ├── etc/
    ├── etc_default/             ← seed_etc() built this
    │   ├── hostname             (from alpine-base, unchanged)
    │   ├── shells               (from bash, unchanged)
    │   ├── ssh/sshd_config      (from openssh, NEW)
    │   ├── ssh/ssh_config       (from openssh, NEW)
    │   └── zsh/zshrc            (from zsh, NEW)
    │
    └── etc_store/               ← seed_etc() built this
        ├── hostname = "myhost"  (preserved from gen/0/etc_store!)
        ├── shells               (from bash, unchanged)
        ├── ssh/sshd_config      (from openssh, NEW, needs post-install)
        └── zsh/zshrc            (from zsh, NEW)
```

**Conflict detection in seed_etc() (line 122-153)**:

For each `/etc` entry:
```
hostname:
  new_src = alpine-base/etc/hostname (default "localhost")
  baseline = gen/0/etc_default/hostname
  stored = gen/0/etc_store/hostname = "myhost" (user edited!)
  
  edited = stored ≠ baseline → TRUE (user edited)
  
  Action:
    etc_default/hostname = new_src (upstream default)
    etc_store/hostname = stored (KEEP USER'S EDIT)
    → Result: hostname stays "myhost" ✓

ssh/sshd_config:
  new_src = openssh/etc/ssh/sshd_config (NEW package!)
  baseline = none (doesn't exist in gen 0)
  stored = none
  
  Action:
    etc_default/sshd_config = new_src
    etc_store/sshd_config = new_src
    → Result: new sshd config flows through
```

**After activate_generation(1):**

```
/rootfs/kod/generation/
├── 0/                 ← Still exists!
│   └── ...
├── 1/                 ← NOW ACTIVE
│   ├── bin -> ...
│   ├── etc/
│   ├── etc_default/
│   └── etc_store/
│
└── current -> /kod/generation/1   ← Repointed! (atomic operation)
```

Live filesystem:
```
/
├── bin -> /kod/generation/1/bin   ← Points to gen 1
├── usr -> /kod/generation/1/usr   ← Points to gen 1
├── etc                             ← Real dir, merged from gen/1/etc_store
│   ├── hostname = "myhost"       (preserved!)
│   ├── shells                     (bash default)
│   ├── ssh/
│   │   ├── sshd_config           (openssh new)
│   │   └── ssh_config            (openssh new)
│   └── zsh/
│       └── zshrc                 (zsh new)
└── kod/generation/current -> /kod/generation/1
```

**Storage on disk after Gen 1:**
```
Store:          ~350 MB (bash, musl, busybox, linux, openssh, zsh)
Generation 0:   ~2 MB   (symlinks + etc copies)
Generation 1:   ~2 MB   (symlinks + etc copies)
Total:          ~354 MB

Note: If packages were 100 MB each, full-chroot model would be:
  Gen 0: 100 MB
  Gen 1: 100 MB
  Total: 200 MB

With 20 generations:
  Store-based: 350 MB store + 40 MB gens = 390 MB
  Full chroot: 2000 MB (20 × 100 MB)
```

---

## After Phase 3: Rebuild Again (Only Config Changes)

**User edits config**: Still `["alpine-base", "bash", "openssh", "zsh"]` BUT modifies:
- SSH config via Lua (changes generation/etc_store/ssh/sshd_config)
- Network interface IP address

**Result**: No NEW packages to download (apk simulate returns same packages).

**create_generation(2, to_install, prev_generation=1):**

```
/rootfs/kod/generation/
├── 0/ ← Still here
├── 1/ ← Still here
└── 2/ ← NEW
    ├── bin -> /kod/store/bash/5.3.9-r1/bin       ← REUSED
    ├── usr -> /kod/store/bash/5.3.9-r1/usr       ← REUSED
    ├── etc/
    ├── etc_default/                              ← IDENTICAL to gen 1
    │   ├── hostname
    │   ├── ssh/sshd_config (openssh default)
    │   └── ...
    │
    └── etc_store/                                ← seed_etc() from gen 1
        ├── hostname = "myhost"  (from gen 1, unchanged)
        ├── ssh/sshd_config      (from gen 1, but user's edits?)
        │   → If OpenRC post-install modified this in gen 1
        │   → It gets carried forward here
        │   → If user hand-edited in live /etc, those are NOT tracked
        │       (user must re-apply via Lua config or Lua-generate files)
        └── ...
```

**Storage**:
```
Store:  350 MB (no new packages)
Gen 0:  2 MB
Gen 1:  2 MB
Gen 2:  2 MB
Total:  356 MB
```

---

## Rollback Scenario: User Boots Gen 0

**At boot time**, bootloader has entries:

```
menuentry "Alpine Gen 0" {
  linux /kod/generation/0/boot/vmlinuz
  initrd /kod/generation/0/boot/initramfs
  cmdline root=... ro
}

menuentry "Alpine Gen 1" {
  linux /kod/generation/1/boot/vmlinuz
  initrd /kod/generation/1/boot/initramfs
  cmdline root=... ro
}

menuentry "Alpine Gen 2" {
  linux /kod/generation/2/boot/vmlinuz
  initrd /kod/generation/2/boot/initramfs
  cmdline root=... ro
}
```

**User selects "Gen 0"** at boot.

**System boots with Gen 0's kernel and reads symlinks:**
```
/bin → /kod/generation/0/bin → /kod/store/bash/5.3.9-r1/bin
/usr → /kod/generation/0/usr → /kod/store/bash/5.3.9-r1/usr
/etc → Gen 0's etc (hostname = "localhost", no openssh, etc.)
```

**Result**: System runs Gen 0. openssh not available. Everything as it was.

---

## Runtime Rollback Scenario (Option B: Runtime restore)

**While running Gen 2, user realizes: "I want Gen 0 back NOW (no reboot)"**

**If generation-apks implemented this** (it doesn't, but our Alpine Kodos should):

```python
def activate_generation_runtime(root, generation):
    # 1. Point symlinks to new generation
    current = root / "generation" / "current"
    current.unlink()
    symlink_chroot(current, generation)
    
    # 2. Merge etc into live filesystem
    activate_etc(generation / "etc", live_etc, ...)
    
    # 3. Tell systemd (or OpenRC) to reload
    #    (Some services auto-respond to filesystem changes, some don't)
    
    # 4. Services that need restart: openssh is now available (was unavailable in gen 0)
    #    → chroot_exec("rc-service sshd start")
    
    # 5. Services that need stop: zsh was available, now isn't
    #    → chroot_exec("rc-service zsh stop") ← if zsh had a service
```

**This is tricky** because:
- Kernel modules can't reload (need reboot)
- Running processes still have old library handles
- Some services don't auto-detect filesystem changes

**Simpler approach (what Kodos probably does)**:
- Reboot to activate (clean state)
- Or: user runs `kodos rollback gen-0` which reboots

---

## /etc Handling in Detail

Let's trace what happens to `/etc/ssh/sshd_config` through generations:

### Gen 0 (Fresh install, OpenRC post-install runs):

```
Store (read-only):
  openssh/8.9p1-r2/etc/ssh/
    ├── sshd_config      (upstream default, never modified)
    ├── ssh_config
    └── ...

Gen 0 before post-install:
  etc_default/ssh/sshd_config = copy of upstream
  etc_store/ssh/sshd_config = copy of upstream
  etc/ssh/sshd_config -> etc_store/ssh/sshd_config

Gen 0 post-install script runs:
  /kod/store/openssh/8.9p1-r2/.post-install
  → Modifies /etc/ssh/sshd_config (which is a link to etc_store)
  → Appends: "PermitRootLogin no" or generates keys

Gen 0 result:
  etc_default/ssh/sshd_config = original (baseline)
  etc_store/ssh/sshd_config = original + post-install changes
  /etc/ssh/sshd_config -> etc_store version (with changes)
```

### Gen 1 (Rebuild with same openssh version, but we edit sshd_config):

```
seed_etc(gen1, to_install, gen0):
  new_src = openssh/8.9p1-r2/etc/ssh/sshd_config (same!)
  baseline = gen0/etc_default/ssh/sshd_config
  stored = gen0/etc_store/ssh/sshd_config (has post-install changes)
  
  edited = stored ≠ baseline?
  → YES (post-install added lines)
  
  Action:
    gen1/etc_default/ssh/sshd_config = new_src (same as gen0)
    gen1/etc_store/ssh/sshd_config = stored (keep gen0's version with changes!)
  
  Result: sshd_config with post-install changes flows forward
```

### Gen 1.5 (User hand-edits sshd_config in live /etc):

```
User: vi /etc/ssh/sshd_config
Adds: "LogLevel VERBOSE"

System running on Gen 1:
  /etc/ssh/sshd_config -> etc_store version (editable link)
  ← Now contains "LogLevel VERBOSE"
```

### Gen 2 (Rebuild again):

```
Problem: How does "LogLevel VERBOSE" get into Gen 2?

Option A: Re-run seed_etc()
  → etc_store is rebuilt from Gen 1's etc_store (post-install state)
  → User edits to LIVE /etc are NOT tracked
  → "LogLevel VERBOSE" is LOST

Option B: Make user edits explicit in Lua config
  → Lua templates generate /etc/ssh/sshd_config content
  → seed_etc() can incorporate these
  → Cleaner, more reproducible

Option C: Track live /etc changes (hard)
  → Need to diff live /etc vs. generation's etc
  → Apply diffs to next generation
  → Complex, error-prone
```

**generation-apks doesn't solve this** — it assumes:
1. Post-install scripts do the configuration
2. User rarely hand-edits /etc
3. If they do, they use Lua templates to regenerate

For Alpine Kodos, we should probably:
1. Make Lua generate critical /etc files
2. Warn if /etc is hand-edited but not in config
3. Optionally preserve live /etc changes if user opts in

---

## Deletion Scenario: Remove OpenSSH

**Gen 2**: `packages = ["alpine-base", "bash", "openssh", "zsh"]`

**Gen 3**: `packages = ["alpine-base", "bash", "zsh"]` (removed openssh)

```python
apk add --simulate alpine-base bash zsh
# Returns: bash, musl, busybox, zsh, ... (NO openssh)
```

**create_generation(3, to_install, prev_generation=2):**

```
Gen 3:
  ├── bin -> bash/bin              ← Still there
  ├── etc/
  ├── etc_default/                 ← seed_etc():
  │   ├── hostname                 (from alpine-base)
  │   ├── shells                   (from bash)
  │   ├── ssh/sshd_config          (openssh shipped this; gen 2 had it)
  │   │                             → Check: did user edit it?
  │   │                             → stored ≠ baseline?
  │   │                             → If yes: KEEP IT (carry forward)
  │   │                             → If no: don't carry (upstream dropped it)
  │   └── zsh/zshrc                (from zsh)
  │
  └── etc_store/                   ← seed_etc() logic:
      ├── hostname                 (from baseline)
      ├── shells                   (from baseline)
      ├── ssh/sshd_config          (if user edited in gen 2, preserved!)
      └── zsh/zshrc                (from upstream)
```

**Code from seed_etc() (lines 148-152)**:

```python
elif edited:
    # upstream dropped the file but it was edited (often a post-install
    # append like /etc/shells): carry the edit forward as new baseline
    copy_etc_entry(store_src, dst_default)
    copy_etc_entry(store_src, dst_store)
```

**This is smart**: If you customized `/etc/ssh/sshd_config`, even though openssh is gone, your customization is preserved (assuming you want to keep the config for potential reinstall).

---

## Summary: What's on Disk

After all this:

```
/rootfs/kod/
├── cache/                          ← Downloaded .apk files (temporary)
│   ├── bash-5.3.9-r1.apk
│   ├── openssh-8.9p1-r2.apk
│   └── zsh-5.9-r3.apk
│
├── store/                          ← Package library (read-only)
│   ├── bash/5.3.9-r1/
│   │   └── files/ (bin, usr, etc, var)
│   ├── openssh/8.9p1-r2/
│   │   └── files/
│   └── zsh/5.9-r3/
│       └── files/
│
└── generation/                     ← Generation directories
    ├── 0/
    │   ├── bin -> /kod/store/bash/5.3.9-r1/bin
    │   ├── etc/ (merged)
    │   ├── etc_default/
    │   └── etc_store/
    ├── 1/
    │   ├── bin -> /kod/store/bash/5.3.9-r1/bin
    │   ├── etc/
    │   ├── etc_default/
    │   └── etc_store/
    ├── 2/
    ├── 3/
    └── current -> /kod/generation/3 ← Active generation (atomic pointer)
```

**Disk usage**:
- Store: ~350 MB (all packages, extracted once)
- 4 generations: ~8 MB (symlinks + /etc copies)
- **Total: ~358 MB**

vs. Full-chroot model:
- 4 generations × 100 MB each: **400 MB**

With 20 generations:
- Store-based: 350 MB + 40 MB = **390 MB**
- Full-chroot: 20 × 100 MB = **2000 MB**

---

## Key Takeaway

**generation-apks is not building an Alpine installer. It's building a generation management system.**

The three core ideas:
1. **Package store**: Download packages once, use many times
2. **Symlink generations**: Each generation is lightweight (mostly symlinks + /etc)
3. **/etc conflict resolution**: Track edits vs. upstream changes, preserve user customizations

This is **production-quality code** for managing multiple system generations. Your Alpine Kodos should port this (or start simpler and evolve to this).
