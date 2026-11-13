# Generation Storage: Quick Reference & Decision Guide

## What generation-apks Actually Does

**generation-apks is NOT just about extracting APK files.** It's a complete generation management system that:

1. **Downloads packages** from Alpine repos
2. **Extracts them** into a content-addressed store (`/kod/store/`)
3. **Creates lightweight generation directories** using symlinks pointing into the store
4. **Handles `/etc` specially** with three trees (default/store/live) to track config changes
5. **Activates generations atomically** by repointing a symlink
6. **Detects config conflicts** when upgrading (user edit vs. upstream change)
7. **Preserves user edits** across generations

## The Storage Model: Three Layers

```
Layer 1: Package Store (Read-Only)
└─ /kod/store/bash/5.3.9-r1/files/
   └─ /bin, /usr, /etc, /var extracted from .apk

Layer 2: Generation Skeleton (Symlinks + /etc)
└─ /kod/generation/0/
   ├─ /bin → /kod/store/bash/5.3.9-r1/bin
   ├─ /usr → /kod/store/bash/5.3.9-r1/usr
   ├─ /etc (real directory, mutable)
   ├─ /etc_default (pristine package defaults)
   └─ /etc_store (working copy, editable)

Layer 3: Atomic Pointer
└─ /kod/generation/current → /kod/generation/1
   (one symlink change = instant rollback)
```

## /etc Handling: The Clever Part

Each generation keeps THREE copies of config files:

| Tree | Purpose | How it's used |
|------|---------|---|
| `etc_default/` | Pristine upstream defaults | Baseline for conflict detection |
| `etc_store/` | Working copy (mutable) | What actually runs; has post-install changes |
| `etc/` | Live filesystem | Symlinks into etc_store |

**Conflict Detection**: When upgrading, for each `/etc` file:
```
if (new_upstream_exists && user_edited):
    keep_user_edit()
    warn("conflict: kept user edit, new upstream in .apknew-1")
elif (upstream_dropped && user_edited):
    carry_forward_edit()
    note("upstream dropped but you edited, preserving")
else:
    use_new_default()
```

This is **NixOS/Guix logic** — conflicts are detected and reported, never silently lost.

## Storage Comparison: Three Approaches

### Approach A: Full Chroot Per Generation (Simplest)
```
Gen 0: [all files]                           = 500 MB
Gen 1: [all files]                           = 500 MB
Gen 2: [all files]                           = 500 MB
────────────────────────────────────────────────
Total with 20 gens: 10 GB
```

**Pros**: Simple to understand and implement, clear what's in each gen  
**Cons**: Massive storage overhead  
**Good for**: MVP (start here)

### Approach B: Store-Based (What generation-apks Does)
```
Store: [bash, openssh, zsh, etc.]           = 500 MB (once)
Gen 0: [symlinks + /etc copies]             = 3 MB
Gen 1: [symlinks + /etc copies]             = 3 MB
Gen 2: [symlinks + /etc copies]             = 3 MB
────────────────────────────────────────────────
Total with 20 gens: 500 + 60 = 560 MB
```

**Pros**: Excellent deduplication, instant rollback, tracks config changes  
**Cons**: Symlink management complexity, /etc handling intricate  
**Good for**: Production (migrate to this)

### Approach C: Btrfs Snapshots (Like Kodos)
```
Snapshot 0: [CoW reference to base]
Snapshot 1: [CoW reference + delta]
Snapshot 2: [CoW reference + delta]
────────────────────────────────────
Total with 20 gens: 500 + (deltas)
```

**Pros**: Filesystem handles CoW, super efficient, easy rollback  
**Cons**: Requires Btrfs, less portable, Alpine complexity  
**Good for**: v2+ (after MVP works)

## Implementation Recommendation for Alpine Kodos

### Phase 1: MVP (Weeks 1-8)
**Use Approach A: Full Chroot Per Generation**
```
/var/lib/kodos-alpine/generations/
├── 0-base-install/
│   ├── bin/
│   ├── usr/
│   ├── etc/
│   └── manifest.json
├── 1-add-zsh/
│   └── ... (complete copy)
└── current -> 0-base-install (symlink)
```

**Why**: Fastest to implement, no symlink debugging, clear semantics  
**Drawback**: Storage overhead (acceptable for MVP)

### Phase 2: v1.0 (Weeks 12-16)
**Migrate to Approach B: Store-Based (from generation-apks)**
```
/var/lib/kodos-alpine/
├── store/                    (packages, read-only)
├── generations/0/            (symlink skeleton + /etc)
└── generations/1/            (symlink skeleton + /etc)
```

**Why**: User feedback may show storage is bottleneck  
**How**: Port `generation-apks` code incrementally

## Key Code to Port from generation-apks

| Function | Purpose | Complexity |
|----------|---------|---|
| `create_generation()` | Create generation directory + symlinks | Low |
| `seed_etc()` | Detect config conflicts + preserve edits | Medium |
| `activate_generation()` | Switch current generation atomically | Low |
| `chroot_resolve()` | Resolve symlinks as kernel would | High |

## Decision Matrix: Which Approach for YOU?

| Question | Answer → Approach |
|----------|---|
| "How many generations do users need?" | <5 → Approach A, ≥5 → Approach B |
| "Is storage capacity a concern?" | Yes → Approach B, No → Approach A |
| "When should rollback happen?" | At boot → Any, At runtime → Approach A better |
| "How fast can you code MVP?" | Important → Approach A |
| "Do you need /etc conflict detection?" | Yes → Approach B, No → Approach A |

## What generation-apks Solves vs. What It Doesn't

### ✅ Solves
- Package deduplication via store
- Generation tracking (what's in each)
- /etc conflict detection (user edit vs. upstream)
- Atomic generation switching (symlink repoint)
- Rollback at boot time (bootloader entries)
- Storage efficiency (90% savings vs. full chroot)

### ❌ Doesn't Solve
- Bootloader configuration (grub/extlinux entries)
- Runtime service management (stopping/starting services when gen changes)
- Package dependency tracking (store has packages but not dependency tree)
- Partial installation recovery (halfway install rollback)
- Kernel module reloading (needs reboot anyway)

### ⚠️ Requires Design
- Generation metadata format (what's in each generation)
- Post-install script tracking (what changed, can we re-run?)
- User edit persistence (how to carry edits forward)
- Config template generation (Lua → /etc files)

## Concrete Example: Gen 0 → Gen 1 with generation-apks

```python
# Gen 0 created with: packages = ["alpine-base", "bash"]
to_install = {
    "bash": "5.3.9-r1",
    "musl": "1.2.5-r0",
    "busybox": "1.37.0-r0",
    ...
}

# Gen 1 created with: packages = ["alpine-base", "bash", "openssh"]
to_install = {
    "bash": "5.3.9-r1",      # Same, reuse from store
    "musl": "1.2.5-r0",      # Same, reuse from store
    "busybox": "1.37.0-r0",  # Same, reuse from store
    "openssh": "8.9p1-r2",   # NEW, download + extract
    ...
}

# Storage impact:
# Gen 0: 100 MB (full files)
# Gen 1: +5 MB (openssh only), +2 MB (/etc copies)
# Total: 107 MB (vs. 200 MB with full chroot)
```

## Directory Structure You Should Implement

### Minimum (Approach A):
```
/var/lib/kodos-alpine/
├── generations/
│   ├── 0-2026-11-10-abc123/
│   │   ├── bin/
│   │   ├── usr/
│   │   ├── etc/
│   │   ├── manifest.json
│   │   └── boot-files/
│   └── current -> 0-2026-11-10-abc123/
├── boot/              (bootloader configs)
│   └── grub.cfg
└── logs/
    └── generation-0.log
```

### Advanced (Approach B):
```
/var/lib/kodos-alpine/
├── store/
│   ├── bash/5.3.9-r1/files/
│   ├── openssh/8.9p1-r2/files/
│   └── ... (read-only)
├── generations/
│   ├── 0/
│   │   ├── bin -> /var/lib/kodos-alpine/store/bash/.../bin
│   │   ├── usr -> ...
│   │   ├── etc/           (real, mutable)
│   │   ├── etc_default/   (pristine)
│   │   └── etc_store/     (working)
│   └── current -> 0/
└── metadata/
    ├── generation-0.json
    └── generation-1.json
```

## What to Do Next

1. **Review the three docs I created**:
   - `GENERATION_STORAGE_DETAILED.md` (how it works)
   - `GENERATION_STORAGE_WALKTHROUGH.md` (visual examples)
   - This document (decision guide)

2. **Decide your MVP approach**:
   - Start with Approach A (full chroot) for speed
   - Plan migration to Approach B later

3. **If you choose Approach B**, study these functions in `install-apks.py`:
   - Line 289: `create_generation()`
   - Line 94: `seed_etc()`
   - Line 269: `activate_generation()`
   - Line 169: `chroot_resolve()`

4. **Design metadata format** (generation-apks is missing this):
   ```json
   {
     "generation": 0,
     "timestamp": "2026-11-10T10:30:00Z",
     "config_hash": "sha256:abc123...",
     "kernel": "linux-lts-6.8.0-r0",
     "packages": {
       "bash": "5.3.9-r1",
       "openssh": "8.9p1-r2"
     },
     "boot_entry_id": "gen-0-abc123"
   }
   ```

5. **Start coding** with the MVP (Approach A) and don't get fancy until you have a working system.

## The Bottom Line

**generation-apks uses a hybrid of all three approaches** — it's production-quality code for generation management. For Alpine Kodos:

- **MVP**: Simple full-chroot per generation (easy to code, obvious what's where)
- **v1.0**: Migrate to store-based like generation-apks (storage efficient, elegant)
- **v2**: Consider Btrfs snapshots if users demand more (less portable but best efficiency)

Start with MVP. You can always evolve. The generation-apks code will be your reference for v1 migration.
