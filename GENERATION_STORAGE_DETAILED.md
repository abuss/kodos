# Generation Storage in Detail: How generation-apks Works

## Executive Summary

**generation-apks uses a hybrid approach:**
- **Content-addressed package store** (like Nix) at `/kod/store/<pkg>/<version>/files/`
- **Symlink-based generation filesystem** that points into the store
- **Special handling for `/etc`** which is mutable and can vary per generation
- **Atomic activation** via symlink pointing to the current generation

This is elegant because it **deduplicates packages** (bash-5.3.9-r1 exists once), **tracks what changed** (compare generations), and **allows rollback** (just repoint the "current" symlink).

---

## The Three-Layer Architecture

### Layer 1: Package Store (Read-Only, Content-Addressed)

```
/rootfs/kod/store/
├── bash/
│   └── 5.3.9-r1/
│       ├── bin/
│       ├── etc/
│       ├── usr/
│       ├── var/
│       └── .PKGINFO (metadata)
├── busybox/
│   └── 1.37.0-r0/
│       ├── bin/
│       ├── etc/
│       ├── ...
└── zsh/
    └── 5.9-r3/
        ├── bin/
        ├── etc/
        └── ...
```

**How packages get here:**
1. Download `.apk` file from Alpine repository
2. Extract `.apk` (it's just `tar.gz`) into `store/<pkg>/<version>/files/`
3. Exclude metadata (`.PKGINFO`, `.SIGN.*`, post-install scripts)
4. Result: **Read-only filesystem mirror** of what the package ships

**Key properties:**
- ✅ Immutable (packages never change after extraction)
- ✅ Content-addressed (bash-5.3.9-r1 is unique and appears once)
- ✅ Deduplication works (if system 1 and system 2 both need bash-5.3.9-r1, they point to same store entry)
- ✅ Introspectable (can query what's in each version without database)

---

### Layer 2: Generation Directories (Symlink-Based Rootfs)

Each generation is a **skeleton filesystem** where most entries are **symlinks into the store**:

```
/rootfs/kod/generation/
├── 0/                              # Generation 0 (fresh install)
│   ├── bin -> /kod/store/bash/5.3.9-r1/bin
│   ├── usr -> /kod/store/bash/5.3.9-r1/usr
│   ├── var -> /kod/store/busybox/1.37.0-r0/var
│   ├── boot -> /kod/store/linux-lts/6.8.0-r0/boot
│   ├── etc/                        # Real directory (mutable!)
│   │   ├── ssh/
│   │   ├── hostname
│   │   ├── fstab
│   │   └── ...
│   ├── etc_default/                # Pristine copies of what packages ship
│   │   ├── ssh/
│   │   └── ...
│   └── etc_store/                  # Working copy (editable)
│       ├── ssh/
│       └── ...
│
├── 1/                              # Generation 1 (added zsh)
│   ├── bin -> /kod/store/bash/5.3.9-r1/bin  (same as gen 0!)
│   ├── bin -> /kod/store/zsh/5.9-r3/bin     (NEW!)
│   ├── usr -> /kod/store/bash/5.3.9-r1/usr
│   ├── etc/                        # Real directory
│   ├── etc_default/                # Pristine copies
│   └── etc_store/                  # Working copy
│
└── current -> /kod/generation/0    # Symlink to active generation
```

**Why symlinks instead of copying?**
- If bash-5.3.9-r1 is in store, both gen 0 and gen 1 symlink to it
- Gen 0 uses maybe 100 MB of store, Gen 1 reuses most of it (only new packages add storage)
- Multiple generations on same disk = minimal storage overhead
- "Rolling back" = just repoint the `current` symlink (instant, atomic)

**How activation works:**
```python
def activate_generation(root, generation):
    current = root / "generation" / "current"
    current.unlink()  # Atomically clear old symlink
    symlink_chroot(current, generation)  # Point to new generation
```

This is **one atomic filesystem operation** — either old generation or new is active, never half-way.

---

### Layer 3: Mutable Configuration (`/etc`)

**The Problem**: `/etc` contains **user edits** (hand-modified configs) and **package post-install changes** (e.g., bash adds itself to `/etc/shells`). These are **mutable** and **can't be in the read-only store**.

**The Solution**: Each generation has **three etc trees**:

#### a) `etc_default/` — Pristine Package Defaults
```
generation/0/etc_default/
├── ssh/sshd_config          (from openssh package, untouched)
├── hostname                 (from alpine-base package)
├── fstab                    (from alpine-base package)
└── shells                   (from bash package, untouched)
```

Created by copying what each package ships in `store/<pkg>/<version>/etc/`. First package (alphabetically) wins for conflicts.

#### b) `etc_store/` — Working Copy (Editable)
```
generation/0/etc_store/
├── ssh/sshd_config          (maybe edited by post-install or by hand)
├── hostname
├── fstab
└── shells                   (bash post-install appended /bin/bash here)
```

This is what actually runs. Post-install scripts write here. Users hand-edit here.

#### c) `etc/` — Activation Layer
```
generation/0/etc/
├── ssh/sshd_config -> /kod/generation/0/etc_store/ssh/sshd_config
├── hostname -> /kod/generation/0/etc_store/hostname
├── fstab -> /kod/generation/0/etc_store/fstab
└── shells -> /kod/generation/0/etc_store/shells
```

Symlinks into `etc_store/`. This is what the live system sees (via `activation_etc()`).

---

## Bridging Generations: Config Conflict Resolution

**Scenario**: You upgrade from bash-5.3.9-r1 to bash-5.4.0-r1, but you hand-edited `/etc/shells`.

**What should happen?**
- New bash ships a new default `/etc/shells` (upstream changes)
- Your hand edit should NOT be silently replaced
- But you should know a new default exists

**Solution**: `seed_etc()` compares three versions:

```python
def seed_etc(generation, to_install, prev_generation):
    # For each file that exists in prev or new:
    
    for file in (new_packages ∪ prev_packages):
        baseline = prev_generation/etc_default/file
        current_edit = prev_generation/etc_store/file
        new_upstream = to_install_packages/etc/file
        
        edited = (current_edit exists and differs from baseline)
        
        if new_upstream exists and NOT edited:
            # No conflict: new default flows through
            etc_default/file = new_upstream
            etc_store/file = new_upstream
            
        elif new_upstream exists and edited:
            # CONFLICT: keep your edit, save new default nearby
            etc_default/file = new_upstream
            etc_store/file = current_edit (unchanged)
            etc_default/file.apknew-1 = new_upstream (warning!)
            print("config conflict: kept local edit of etc/shells;")
            print("new default in etc_default/shells.apknew-1")
            
        elif edited and NOT new_upstream:
            # Upstream dropped the file but you edited it
            # Assume you want to keep it
            etc_default/file = current_edit (new baseline)
            etc_store/file = current_edit
            
        else:
            # Nobody ships it, never edited -> prune it
            # (generation/etc activation will remove)
```

This is **NixOS/Guix logic**: conflicts are detected and reported, not silently resolved.

---

## Complete Data Flow: From Fresh Install to Rebuild to Rollback

### Phase 1: Fresh Install → Generation 0

```
1. User specifies: packages = ["alpine-base", "bash", "openssh"]

2. get_list_of_package_to_install()
   → Run: apk add --simulate alpine-base bash openssh
   → Parse output: bash 5.3.9-r1, openssh 8.9p1-r2, musl 1.2.5-r0, ...
   → Result: to_install = {bash: 5.3.9-r1, musl: 1.2.5-r0, ...}

3. get_package_versions_urls()
   → Run: apk policy bash musl openssh ...
   → Parse: alpine-base (from repo v3.24/main)
   → Build: url_of = {(bash, 5.3.9-r1): "https://...", ...}

4. download_unpack()
   → For each package:
      - Download: bash-5.3.9-r1.apk
      - Extract to: /kod/store/bash/5.3.9-r1/files/
      - Exclude: .PKGINFO, .SIGN.*, post-install scripts

5. create_generation(root=0, to_install)
   → Create directory: generation/0/
   → For each package:
      - Symlink: generation/0/bin -> /kod/store/bash/5.3.9-r1/bin
      - Symlink: generation/0/usr -> /kod/store/bash/5.3.9-r1/usr
      - (but NOT etc, it gets special treatment)
   → seed_etc(generation/0, to_install, prev_generation=None)
      - Extract etc from packages into generation/0/etc_default/
      - Copy to generation/0/etc_store/
   → Result: generation/0 is a symlink skeleton + mutable /etc

6. activate_generation(root, generation/0)
   → current -> generation/0
   → activate_etc(generation/0/etc, /etc, ...)
      - Merge generation/0/etc tree into live /etc
   → For each toplevel dir in generation/0:
      - Symlink: /bin -> /kod/generation/0/bin
      - Symlink: /usr -> /kod/generation/0/usr
      - /etc is real and merged (see above)

7. mount_chroot() + chroot + run post-install scripts
   → For bash: run /kod/store/bash/5.3.9-r1/.post-install
      - Appends /bin/bash to etc_store/shells
   → For openssh: run /kod/store/openssh/8.9p1-r2/.post-install
      - Creates ssh keys in etc_store/ssh/

8. umount_chroot()
   → System ready
```

**Result after Gen 0**:
```
/                    (chroot root)
├── bin -> /kod/generation/0/bin
├── usr -> /kod/generation/0/usr
├── etc              (real, merged from generation/0/etc_store/)
├── kod/generation/current -> /kod/generation/0/
└── kod/store/       (packages, read-only)
```

---

### Phase 2: Rebuild → Generation 1

User edits config: `packages = ["alpine-base", "bash", "openssh", "zsh"]`

```
1. to_install = apk simulate alpine-base bash openssh zsh
   → Result: bash 5.3.9-r1, openssh 8.9p1-r2, zsh 5.9-r3, ... (same + zsh)

2. download_unpack(to_install)
   → bash, openssh, musl: already in store (skip if exists)
   → zsh: NEW, download and extract to /kod/store/zsh/5.9-r3/

3. create_generation(root=1, to_install, prev_generation=0)
   → Create directory: generation/1/
   → Symlink same packages as before:
      - generation/1/bin -> /kod/store/bash/5.3.9-r1/bin
      - generation/1/usr -> /kod/store/bash/5.3.9-r1/usr
   → Symlink NEW package:
      - generation/1/bin -> /kod/store/zsh/5.9-r3/bin (added!)
   → seed_etc(generation/1, to_install, prev_generation=0)
      - Load prev's etc edits from generation/0/etc_store/
      - Load new defaults from packages (zsh may ship /etc/zsh/zshrc)
      - Compare: did user edit anything in gen 0?
      - If no conflicts: generation/1/etc_store/ = gen 0's edits + zsh defaults
      - If conflicts: detect and report

4. activate_generation(root, generation/1)
   → current -> generation/1 (ATOMIC!)
   → activate_etc(...)
   → System now running on Gen 1

5. mount_chroot() + chroot + post-install scripts
   → zsh .post-install runs (if it has one)
   → bash, openssh scripts already ran in Gen 0 (don't re-run)

6. Result:
   → Gen 0 still exists (/kod/generation/0/)
   → Gen 1 reuses bash, openssh from store (no duplication)
   → Gen 1 adds zsh (minimal storage)
   → Gen 0 had ~100 MB, Gen 1 adds ~5 MB (zsh)
   → Current system is Gen 1
```

---

### Phase 3: Rollback to Gen 0

```
1. User: kodos rollback generation-0

2. activate_generation(root, generation/0)
   → current -> generation/0 (ATOMIC!)

3. System is now on Gen 0 again

4. Next boot: zsh is gone, system as it was
```

**This is instant** — just one symlink change. No copying, no extraction, no post-install re-runs.

---

## Storage Efficiency: Why This Matters

### Comparison: Full Chroot Per Generation vs. Store-Based

**Full Chroot Model** (Option B from architecture doc):
```
Generation 0: [bash, openssh, musl, linux, ...]  = 500 MB
Generation 1: [bash, openssh, musl, linux, zsh] = 505 MB
---
Total: 1000 MB
```

**Store-Based Model** (what generation-apks does):
```
Store:  [bash, openssh, musl, linux, zsh]         = 505 MB (all packages once)
Gen 0:  [symlinks + etc_default + etc_store]    = 2-5 MB
Gen 1:  [symlinks + etc_default + etc_store]    = 2-5 MB
---
Total: ~515 MB (one package library, lightweight generation overlays)
```

**On 10 generations**:
- Full chroot: 5 GB
- Store-based: 550 MB (still ~505 MB store + lightweight gen metadata)

**With Btrfs snapshots** (Option C):
- Can be even better (copy-on-write at filesystem level)
- But requires Btrfs

---

## Limitations & Trade-offs

### What Store-Based Model Handles Well ✅
- Package deduplication (bash-5.3.9-r1 is stored once)
- Quick rollback (symlink repoint, instant)
- Generation tracking (what's in each gen via symlink traversal)
- Config evolution (etc_default vs etc_store tracks changes)
- Atomic activation (one symlink operation)

### What Store-Based Model Doesn't Handle ⚠️
- **Kernel updates**: If Gen 0 has kernel A and Gen 1 has kernel B, you can't switch at runtime without rebooting. Bootloader multiboot entries still work.
- **Dependencies unclear**: Store contains packages but doesn't track "which version of X was actually used" in a generation. You'd need a manifest file per generation to make this clear.
- **Mount tracking**: If a generation uses an overlayfs or has bind mounts, rolling back doesn't automatically undo those. Needs explicit handling.
- **Partial installation**: If installing packages into the store fails halfway, cleanup is manual.

### What Needs Careful Design
1. **Generation metadata file**: Each generation needs a manifest:
   ```json
   {
     "generation": 0,
     "timestamp": "2026-11-10T10:30:00Z",
     "config_hash": "abc123",
     "kernel": "linux-lts-6.8.0-r0",
     "packages": {
       "bash": "5.3.9-r1",
       "openssh": "8.9p1-r2",
       ...
     },
     "boot_entry": "/boot/vmlinuz-gen0"
   }
   ```

2. **Boot configuration**: Must create grub/extlinux entries that point to each generation's kernel:
   ```
   menuentry "Generation 0" {
     linux /kod/generation/0/boot/vmlinuz-lts
     initrd /kod/generation/0/boot/initramfs-lts
     options root=/dev/vda3 ro
   }
   menuentry "Generation 1" {
     linux /kod/generation/1/boot/vmlinuz-lts
     ...
   }
   ```

3. **Post-install script safety**: Running post-installs can modify etc_store. Must track what was modified and prevent re-running on rollback.

---

## Comparison with Kodos (Arch/Btrfs)

| Aspect | Kodos (Arch + Btrfs) | generation-apks (Alpine + Store) |
|--------|---|---|
| **Package source** | Arch repos + AUR | Alpine repos only |
| **Snapshot tech** | Btrfs subvolume snapshots | Filesystem symlinks + store |
| **Generation storage** | Full chroot per gen (CoW) | Package store + symlink skeleton |
| **Storage efficiency** | Excellent (Btrfs CoW) | Good (deduplication) |
| **Rollback speed** | Fast (snapshot metadata) | Instant (symlink repoint) |
| **Boot entry** | systemd-boot per generation | Grub/extlinux per generation |
| **Config handling** | Lua + NixOS patterns | Lua + etc_default/etc_store |
| **Complexity** | Requires Btrfs | Pure filesystem (any FS) |
| **Filesystem portability** | Btrfs only | Any filesystem (ext4, XFS, ...) |

---

## My Recommendation for Alpine Kodos

For your Alpine Kodos system, I'd recommend a **hybrid approach**:

### Start (MVP): Simple Full-Chroot Per Generation
```
/var/lib/kodos-alpine/generations/
├── 0-alpine-base/
│   ├── bin/, usr/, etc/, ...
│   ├── manifest.json (metadata)
│   └── ACTIVE (marker file if active)
├── 1-add-zsh/
│   └── ...
└── current -> 0-alpine-base/ (symlink)
```

**Why start here?**
- Simplest to implement (just copy/extract packages into gen directories)
- No symlink complexity at first
- Clear what's in each generation (it's all there)
- Still supports all rollback methods (boot entries, runtime restore, rebuild)

**When to optimize** (v2):
- If users complain about storage (10 GBs with 20 generations)
- Then implement store-based model from generation-apks

### Long-term (v2): Store-Based Like generation-apks
Once MVP works, adopt generation-apks' approach:
- Single store at `/var/lib/kodos-alpine/store/`
- Generations are symlink skeletons
- Config conflict handling (etc_default vs etc_store)
- Massive storage savings

---

## Summary Table: Which Storage Model?

| Model | Storage | Speed | Complexity | Rollback | Fit for Alpine |
|-------|---------|-------|-----------|----------|---|
| **Full Chroot** | 📦 High (duplication) | ⚡ Slow copy | 🟢 Simple | ✅ All methods | 🟢 START HERE |
| **Store-Based** | 📦📦 Excellent (dedup) | ⚡⚡ Instant relink | 🟡 Medium | ✅ All methods | 🟡 DO LATER |
| **Btrfs Snapshots** | 📦📦 Best (CoW) | ⚡⚡ Snapshot metadata | 🟠 Hard (Btrfs-only) | ✅ All methods | 🔴 NOT YET |

---

## Code References from generation-apks

Key functions you can adapt:

1. **`create_generation()`** (line 289)
   - Creates symlink skeleton pointing into store
   - Handles `etc` specially

2. **`seed_etc()`** (line 94)
   - Compares previous generation's edits with new packages
   - Detects conflicts (user edit vs. upstream change)
   - Carries edits forward

3. **`activate_generation()`** (line 269)
   - Atomically switches current generation
   - Merges etc into live filesystem

4. **`chroot_resolve()`** (line 169)
   - Resolves symlinks as the chroot kernel would
   - Handles absolute symlinks that "dangle" on host

5. **`cow_copy()`** (line 62)
   - Uses `copy_file_range` with CoW filesystem support (Btrfs)
   - Falls back to plain copy on regular filesystems

This is production-quality code that you can port to Go incrementally.

---

## Conclusion

**generation-apks doesn't pick one approach — it uses the best parts of all three:**

1. **Package store** (like Nix, Option A)
2. **Symlink-based generations** (like NixOS, Option A)
3. **Mutable /etc handling** (like Guix, unique)
4. **Config conflict resolution** (user edits vs. upstream, like NixOS)

For your Alpine Kodos, **start with full-chroot (simpler), migrate to store-based (better)** when MVP is stable. The generation-apks code is your reference implementation — it already solves the hard problems (etc_default/etc_store, conflict detection, chroot symlink resolution).
