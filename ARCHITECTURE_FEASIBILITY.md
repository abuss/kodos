# Alpine Kodos: Feasibility & Architecture

**Status**: Architectural Evaluation  
**Scope**: Building Alpine-based declarative system management tool  
**Date**: 2026-11-10

---

## Executive Summary

**✅ HIGHLY FEASIBLE** — All required technologies exist and the four linked projects contain most of the building blocks. The main challenge is **integration and sequencing**, not technology viability. Building this is realistic within 2-4 months depending on desired initial scope.

### Key Feasibility Factors
- ✅ Lua config processing (`proc-lua-conf`) is solid and stable
- ✅ Alpine installation generation (`install-alpine`) is well-researched with comprehensive docs
- ✅ APK package handling (`generation-apks`) has working solutions, minor unresolved issues
- ✅ Go executor is the right language choice (performance, deployment, generation management)
- ⚠️ Integration layer (combining all pieces) must be designed
- ⚠️ Generation storage model needs clarification (your answers suggest hybrid approach)

---

## Three Implementation Approaches

### Approach 1: **Monolithic Go Executor** (Recommended)
**Thesis**: Single Go binary processes Lua → commands → generations, managing all state internally

#### How it works
```
User writes config.lua
       ↓
Go CLI (kodos-alpine) reads config
       ↓
Embedded Lua interpreter executes config (lua/go bindings)
       ↓
Config processing + merging (via proc-lua-conf ported to Go or called via subprocess)
       ↓
Go emits commands (apk add, rc-update, etc.)
       ↓
Go executor runs commands in chroot/target (generation 0 = fresh install, generation N = incremental)
       ↓
Go manages generations (snapshots, store, metadata)
       ↓
Go handles rollback (bootloader config, runtime restore, rebuild from config)
```

#### Strengths
- Single dependency, single binary deployment
- Full control over generation lifecycle in one process
- Lua → commands → execution tightly integrated
- Easy to add new command types without external tools
- Go handles concurrency, generation locking, atomic operations well

#### Weaknesses
- All logic in one place (higher cognitive load initially)
- Requires embedding Lua in Go (adds C-binding complexity)
- Generation storage must be designed before coding

#### Effort: 6-8 weeks (including tests + documentation)

---

### Approach 2: **Lua Generator + Go Executor** (Pipeline)
**Thesis**: Lua generates a command plan file (JSON/TOML), Go executor runs the plan

#### How it works
```
User writes config.lua
       ↓
Lua script processes config (standalone Lua or Python wrapper)
       ↓
Emits plan.json: { commands: [...], metadata: {...}, generation_id: "..." }
       ↓
Go CLI reads plan.json
       ↓
Go executes commands with rollback points
       ↓
Go manages generations (snapshots, store)
       ↓
Go handles rollback
```

#### Strengths
- Lua and Go are completely decoupled (easier to debug)
- Plan is explicit and auditable (user can inspect/modify plan before running)
- Easier to add non-standard commands without modifying executor
- Lower Lua-Go integration complexity

#### Weaknesses
- Two processes, plan file must be synchronized
- Harder to make executor interactive during generation
- Conditional command generation requires plan to be regenerated
- Adds operational complexity (plan file management)

#### Effort: 8-10 weeks (plan format design + both pieces)

---

### Approach 3: **Modular Services** (Microservices)
**Thesis**: Lua generator, Go executor, storage service, generation tracker as separate services

#### How it works
```
Lua generator service → emits commands to message queue
                               ↓
Go executor service consumes commands, runs in chroot
                               ↓
Generation service manages snapshots, history, rollback
                               ↓
Metadata service tracks what's in each generation
```

#### Strengths
- Each service has one responsibility
- Can scale executor independently
- Easy to add monitoring/logging per component
- Fits a deployment ecosystem (systemd, containers)

#### Weaknesses
- Heavy operational complexity (requires message queue, coordinator)
- Overkill for single-machine tool
- Service discovery, state consistency problems
- More to debug, more failure modes

#### Effort: 12-16 weeks (most complex)

---

## Recommendation: Approach 1 (Monolithic Go Executor)

**Why**: You're building a system tool like Kodos, not a platform. Approach 1 keeps the implementation focused, gives you a single binary to test/deploy, and the tight integration means fewer surprises with generation management. You can always decompose later if needed.

---

## Required Components (Detailed)

### 1. **Lua Configuration Processor**

**Input**: `config.lua` (user-written system description)  
**Output**: Normalized Lua table (merged, validated)  
**Status**: ✅ Mostly done (`proc-lua-conf` is stable)

#### What's needed
- Port `proc-lua-conf`'s deep_merge logic to Go, OR
- Call Lua from Go via bindings (e.g., `gopher-lua`, `golua`)
- Add validation layer (check required fields, type checking)
- Support configuration inheritance/overlays (base + user overrides)

#### Requirements
- Type-safe: bools, strings, tables, arrays clearly distinguished
- Error reporting: line numbers, field names in config
- Extensibility: user can define custom Lua functions (like current Kodos)

#### Estimated effort: **1 week** (mostly reuse from `proc-lua-conf`)

---

### 2. **Command Generator**

**Input**: Processed Lua config  
**Output**: List of commands to execute (shell, apk, rc-update, etc.)  
**Status**: ✅ Partially done (`install-alpine` has 80% of logic)

#### What's needed
- Adapt `install-alpine`'s generator logic to Go
- Generate commands for:
  - **Phase 0** (first install): Partitioning, chroot setup, base installation
  - **Phase N** (rebuilds): Incremental package changes, config updates, service changes
  - **Phase X** (any generation): APK add/del, rc-update enable/disable, config file writes
- Handle Alpine-specific operations:
  - `apk add / apk del` with version pinning
  - `rc-update add/del SERVICE RUNLEVEL`
  - Config file generation (sshd_config, network configs, etc.)
  - User/group management

#### Requirements
- Idempotent: running same commands twice yields same result
- Auditable: user can see exact commands before they run
- Safe defaults: warn on destructive operations
- Conditional generation: if package already installed, skip add

#### Estimated effort: **2-3 weeks** (mostly porting `install-alpine`)

---

### 3. **Command Executor**

**Input**: List of commands + generation metadata  
**Output**: Executed changes in target chroot/system  
**Status**: 🔧 Partially done (`install-alpine` has shell script generation, `generation-apks` has APK experiments)

#### What's needed
- Execute commands safely:
  - Chroot management (mount, unmount, cleanup on error)
  - Command sandboxing (each command in isolation, rollback on failure)
  - Timeout handling
  - Output capture (logs per command)
- Support command types:
  - Shell commands (`bash -c "..."`)
  - APK operations (via apk binary or library)
  - File operations (create, update, delete with checksums)
  - Service configuration (rc-update, init.d)
- Error handling:
  - Retry logic for transient failures
  - Partial failure recovery
  - Clear error messages

#### Requirements
- Run as root (obviously)
- Atomic operations per command
- Full logging
- Dry-run mode (show what would happen, don't execute)

#### Estimated effort: **3-4 weeks** (includes APK library integration, chroot management, error handling)

---

### 4. **Generation Manager**

**Input**: Execution results, generation metadata  
**Output**: Generation stored, tracked, available for rollback  
**Status**: 📋 Requirements from `generation-apks` but not implemented

#### What's needed (based on your "how is managed in generation-apks" answer)
- **Generation storage**: Package/filesystem store as researched in `generation-apks`
  - `store/<package-name>/<version>/files/` layout (content-addressed)
  - OR: Full chroot per generation (simpler, more storage)
  - OR: Btrfs/LVM snapshots (like Kodos)
  
- **Generation tracking**:
  - Metadata file per generation (config, timestamp, packages, changes)
  - Generation index (which is current, which are previous)
  - Boot configuration (grub/systemd-boot entries if bootloader multi-gen support)
  
- **Rollback support** (per your answer: "All methods"):
  - **Bootloader**: Multiple entries in boot config, user selects at boot
  - **Runtime restore**: Go command to activate previous generation
  - **Rebuild from config**: Regenerate old config and rebuild from scratch

#### Requirements
- Thread-safe (Go's concurrency model)
- Persistent storage (filesystem-based, no database initially)
- Fast lookup (index, not O(n) scan)
- Clear semantics (what does "active" mean, what does "rollback" do)

#### Estimated effort: **2-3 weeks** (depends on final storage model choice)

---

### 5. **CLI Interface**

**Input**: User commands and flags  
**Output**: System management actions  
**Status**: 🚫 Not started (but Kodos has examples)

#### What's needed
```bash
kodos-alpine install -c config.lua                    # Initial install
kodos-alpine rebuild -c config.lua                    # Rebuild system
kodos-alpine rebuild --dry-run -c config.lua          # Preview changes
kodos-alpine list-generations                         # Show versions
kodos-alpine rollback <generation-id>                 # Restore version
kodos-alpine show-config                              # Display active config
kodos-alpine shell -p PACKAGE                         # Temp package shell (stretch goal)
```

#### Requirements
- Clear help messages
- Good error messages (not stderr dumps)
- Logging (info, debug modes)
- Non-interactive mode (CI/CD friendly)

#### Estimated effort: **1 week** (using Go CLI frameworks like cobra)

---

### 6. **Testing & Documentation**

#### What's needed
- **Unit tests**: Config processing, command generation, rollback logic
- **Integration tests**: Full install → rebuild → rollback cycle
- **Example configs**: Server, workstation, container base image scenarios
- **User documentation**: Config format, examples, troubleshooting
- **Developer documentation**: Architecture, extending with custom commands

#### Estimated effort: **2-3 weeks** (scattered throughout, not separate phase)

---

## Architecture Diagram

```
┌─────────────────────────────────────────────────────────────┐
│                        CLI Layer (Cobra)                    │
│  install / rebuild / list-gen / rollback / show-config      │
└────────┬────────────────────────────────────────────────────┘
         │
         ↓
┌─────────────────────────────────────────────────────────────┐
│         Main Orchestrator (Go, single process)              │
│  • Parse arguments                                          │
│  • Load & validate config.lua                               │
│  • Route to subcommand handler                              │
└────────┬────────────────────────────────────────────────────┘
         │
    ┌────┴────────────────────────┬─────────────────┬──────────┐
    ↓                             ↓                 ↓          ↓
┌────────────────────┐  ┌──────────────────┐  ┌──────────┐  ┌───────────┐
│ Config Processor   │  │ Command Generator│  │ Executor │  │Generation │
│ (Lua + Merge)      │  │ (Lua → Commands) │  │ (Chroot) │  │ Manager   │
├────────────────────┤  ├──────────────────┤  ├──────────┤  ├───────────┤
│ • gopher-lua       │  │ • Parse config   │  │ • Run    │  │ • Store   │
│ • Type validation  │  │ • APK generator  │  │ • Log    │  │ • Track   │
│ • deep_merge       │  │ • Service config │  │ • Error  │  │ • Rollback│
│ • Inheritance      │  │ • File templating│  │ • Timeout│  │ • Boot cfg│
└────────────────────┘  └──────────────────┘  └──────────┘  └───────────┘
         │                       │                  │             │
         └───────────┬───────────┴──────────────────┴─────────────┘
                     │
         ┌───────────↓──────────────┐
         │  Persistent Storage      │
         ├──────────────────────────┤
         │ /var/lib/kodos-alpine/:  │
         │ • generations/           │
         │ • store/ (packages)      │
         │ • metadata/              │
         │ • boot-config/           │
         └──────────────────────────┘
```

---

## Data Flow: Fresh Install

```
1. User: kodos-alpine install -c config.lua
              │
              ↓
2. CLI reads config.lua
              │
              ↓
3. Config Processor merges/validates
              │
              ↓
4. Command Generator emits Phase 0 commands:
   - Partition disk
   - Create chroot
   - apk add alpine-base
   - apk add kernel packages
   - rc-update add networking boot
   - ... etc
              │
              ↓
5. Executor runs commands in sequence (in chroot):
   - Mount filesystem
   - Execute apk add's
   - Execute file creates
   - Execute service configs
   - Unmount
              │
              ↓
6. Generation Manager creates Generation 0:
   - Store packages (if content-addressed model)
   - Write metadata (timestamp, config hash, packages list)
   - Update boot config (add bootloader entry)
   - Mark as active
              │
              ↓
7. Done. User reboots into fresh system.
```

---

## Data Flow: Rebuild (Incremental)

```
1. User: kodos-alpine rebuild -c config.lua
              │
              ↓
2. CLI reads new config.lua
              │
              ↓
3. Config Processor validates new config
              │
              ↓
4. Generation Manager loads Generation N-1 (active)
              │
              ↓
5. Command Generator compares:
   - config.lua (new) vs Generation N-1 metadata
   - Emits commands for CHANGES ONLY:
     * apk add NEWPKG (if package added)
     * apk del OLDPKG (if package removed)
     * rc-update add NEWSVC (if service added)
     * rc-update del OLDSVC (if service removed)
     * Update config files (if changed)
              │
              ↓
6. Executor runs delta commands:
   - Create rollback point
   - Execute commands
   - If error: rollback to pre-rebuild state
              │
              ↓
7. Generation Manager creates Generation N:
   - Record what changed (delta)
   - Update boot config (add entry for new generation)
   - Option to mark as active (with --activate flag, or if in new generation phase)
              │
              ↓
8. Done. User can reboot to new generation or stay on old one.
```

---

## Data Flow: Rollback

```
Option A: Bootloader (at boot time)
  - Grub / systemd-boot shows multiple entries
  - User selects generation N-K
  - System boots into that generation

Option B: Runtime (while running)
  - User: kodos-alpine rollback <gen-id>
  - Executor restores files (if filesystem model)
  - OR restores from snapshots (if Btrfs model)
  - OR reinstalls packages (if content-addressed model)
  - Regenerates configs
  - Updates boot config to mark as active
  - Reboot or apply live if no kernel change

Option C: Rebuild from config (any time)
  - User: kodos-alpine rebuild -c old-config.lua
  - Treated as normal rebuild, regenerates generation
```

---

## Technology Stack

### Core
- **Language**: Go 1.21+
- **Lua Integration**: `gopher-lua` (pure Go, no C dependencies) or `golua` (cgo, if more perf needed)
- **CLI**: `cobra` (industry standard)
- **Config Format**: Lua (via embedded interpreter)

### Storage
- **Generation storage**: Filesystem (initially)
  - Directories: `/var/lib/kodos-alpine/generations/`
  - Format: `generation-<timestamp>-<config-hash>/`
  - Metadata: JSON files per generation
  
- **Packages**: APK files in `store/` or managed by system APK database

### Execution
- **Target management**: Chroot + Mount utilities
- **APK operations**: System `apk` binary or Go library (`go-apkpkg` if exists, else shell out)
- **Command execution**: Go's `os/exec`
- **Logging**: Go's standard library or `logrus`

### Boot Management
- **Alpine boot**: `mkinitfs`, `extlinux` (or systemd-boot if supported)
- **Generation boot entries**: Modify bootloader config per generation

---

## Remaining Questions & Decisions

### 1. **Generation Storage Model** (CRITICAL)
Must choose before coding:

**Option A**: Content-addressed package store (like `generation-apks` sketches)
- `store/bash/5.3.9-r1/files/{bin,etc,usr,...}`
- Pro: Deduplication, clear version tracking
- Con: Complex symlink management, rebuild needed for boots, no easy runtime kernel swaps

**Option B**: Full chroot per generation
- Each generation is complete `/` filesystem
- Pro: Simple, clear, can switch at boot time
- Con: Storage overhead (10s of GB per generation)

**Option C**: Btrfs/LVM snapshots (like Kodos)
- Base is writable generation N, snapshot to create N+1
- Pro: Storage efficient, atomic rollback
- Con: Requires Btrfs or LVM, Alpine-specific complexity

**Recommendation**: Start with Option B (full chroot per gen) for MVP, plan for Option C (snapshots) later. Option A is interesting but adds too much complexity for v1.

---

### 2. **Configuration Format Details** (IMPORTANT)
Define Lua config schema:

```lua
return {
    -- Device layout
    devices = {
        disk0 = { device = "/dev/vda", ... }
    },
    
    -- Boot configuration
    boot = {
        kernel = "linux-lts",
        modules = { "xhci_pci", ... }
    },
    
    -- Package repos
    repos = {
        main = "https://dl-cdn.alpinelinux.org/alpine/v3.24/main",
        community = "https://dl-cdn.alpinelinux.org/alpine/v3.24/community",
    },
    
    -- Packages to install
    packages = { "bash", "openssh", "vim" },
    
    -- Services to enable
    services = {
        ssh = {
            packages = { "openssh" },
            service_name = "sshd",
            settings = { ... }
        }
    },
    
    -- Users
    users = {
        root = { password = "...", shell = "/bin/ash" },
        app = { ... }
    },
    
    -- Network
    network = {
        hostname = "myhost",
        interfaces = { ... }
    },
    
    -- Locale
    locale = { timezone = "UTC", ... }
}
```

---

### 3. **Dry-Run Behavior** (UX DECISION)
What should `--dry-run` show?

- Option A: Print all commands, don't execute
- Option B: Execute in temporary chroot, show changes, roll back
- Option C: Show delta (what will change) without executing anything

**Recommendation**: Option A (simple, fast) for MVP, upgrade to Option B later.

---

## Success Criteria

### Feasibility: ✅ CONFIRMED
- ✅ All four projects provide reusable code/patterns
- ✅ Go + Lua is a proven combination
- ✅ Generation management is solvable with current tech
- ✅ Alpine-specific tooling (apk, rc-update, etc.) is stable

### MVP (Minimum Viable): 8-10 weeks
- ✅ Install Alpine from config.lua
- ✅ Rebuild with package changes
- ✅ Bootloader multi-generation support
- ✅ Runtime rollback command
- ✅ Basic documentation + examples

### v1.0 (Production-ready): 16-20 weeks
- ✅ All MVP features polished
- ✅ Comprehensive testing (unit + integration)
- ✅ Security audit (running as root, chroot escape risks)
- ✅ Performance optimization
- ✅ User/developer documentation complete
- ✅ Snapshot-based generation management (Option C)
- ✅ User configuration support (shell configs, dotfiles)

---

## What to Reuse from Linked Projects

| Project | Reuse | How |
|---------|-------|-----|
| **kodos** | CLI structure, config patterns, generation concepts | Review command structure, config format inspiration |
| **install-alpine** | Generator logic, service handling, command templates | Port Lua generator to Go, reuse APK command patterns |
| **generation-apks** | APK handling, package store layout, database init | Use as reference for APK operations in executor |
| **proc-lua-conf** | Table merging algorithm, shape detection | Port deep_merge to Go or call via subprocess |

---

## Risk Assessment

### High Confidence ✅
- Lua config processing (proven in Kodos, `proc-lua-conf`)
- Alpine package management (stable, well-documented)
- Go as executor language (mature ecosystem)

### Medium Confidence ⚠️
- Generation storage and rollback (multiple approaches, must pick one)
- Integration of four separate components (design work needed)
- Performance at scale (unclear until tested with large configs)

### Low Confidence 🔴
- None identified. This is a solvable problem with known tech.

---

## Timeline Estimate

### Phase 1: Design & Setup (2 weeks)
- Finalize generation storage model
- Spec out Lua config schema
- Set up Go project structure
- Port `proc-lua-conf` deep_merge to Go

### Phase 2: Core (6 weeks)
- Config processor (Lua → Go)
- Command generator (porting `install-alpine`)
- Basic executor (chroot + apk)
- Generation manager (v1 filesystem-based)

### Phase 3: Features & Polish (3-4 weeks)
- CLI (install, rebuild, rollback, list-gen)
- Bootloader integration
- Error handling & rollback logic
- Documentation + examples

### Phase 4: Testing & Hardening (2-3 weeks)
- Unit tests (each component)
- Integration tests (full cycles)
- Security review
- Edge case handling

**Total MVP: 8-10 weeks**  
**Total v1.0: 14-18 weeks**

---

## Conclusion

**✅ HIGHLY FEASIBLE AND RECOMMENDED**

You have:
1. ✅ Proven Lua config pattern (Kodos + proc-lua-conf)
2. ✅ Detailed Alpine installation docs (install-alpine)
3. ✅ Working APK handling experiments (generation-apks)
4. ✅ A stable config merging library (proc-lua-conf)

All you need to do is:
1. Design generation storage model (decision, not tech risk)
2. Build integration layer (well-scoped engineering)
3. Write the Go executor (straightforward implementation)

**Recommendation**: Start with **Approach 1 (Monolithic Go Executor)**, targeting full chroot per generation (Option B storage) for the MVP. This is the fastest path to a working system with a single binary deployment.

Start Phase 1 (design + setup) immediately. You have all the background knowledge you need from your four projects. The main work is architectural integration, not research.
