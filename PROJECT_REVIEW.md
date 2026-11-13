# Linked Projects Review

This workspace (`kodos-merge`) contains **4 linked projects** via symlinks. All four projects are interconnected parts of a Linux distribution building system called **KodOS**.

---

## Project Overview

### 🎯 Purpose
This is a meta-workspace linking together components of **KodOS** — a declarative Linux distribution management tool using Lua configuration, inspired by NixOS. The projects work together to:
1. Generate APK packages
2. Install Alpine Linux
3. Process Lua configuration
4. Provide the main CLI tool

---

## Linked Projects

### 1. **kodos** (Main CLI Tool)
**Link**: `kodos` → `/home/abuss/Work/devel/kodos`

#### Overview
The primary KodOS tool—a Python CLI for installing and managing Linux distributions declaratively.

#### Key Features
- **Lua-based declarative configuration** for reproducible system setup
- **Btrfs snapshots** for system "generations" (atomic rollbacks/upgrades)
- **Multi-package source support**: Arch, AUR, Flatpak
- **Generation management**: boot into previous system versions
- **kod shell** command: temporary package environments (like `nix shell`)
- **User configuration support**: git integration, dotfile managers

#### Technology Stack
- **Language**: Python
- **Package Management**: Leverages Arch Linux + AUR (not its own ecosystem)
- **Filesystem**: Btrfs snapshots for atomic operations
- **Config Format**: Lua

#### Example Configuration
```lua
return {
    repos = {
        arch = repos.arch_repo("https://mirror.rackspace.com/archlinux"),
        aur = repos.aur_repo("yay", "https://aur.archlinux.org/yay-bin.git"),
    },
    devices = {
        disk0 = disk.disk_definition("/dev/vda", "3GB"),
    },
    -- ... boot, hardware, locale, users, services, packages, etc.
}
```

#### Main Commands
- `kod install` - Install KodOS based on configuration
- `kod rebuild` - Rebuild system installation
- `kod rebuild-user` - Rebuild user configuration
- `kod shell` - Run temporary shell with packages

#### Status
⚠️ **Work in progress** (author uses it personally on laptop + desktop, not production-ready)

#### File Structure
- `src/` - Python source code
- `tests/` - Unit tests (25 tests covering exec, safety, error handling)
- `docs/` - Documentation
- `example/` - Example configurations
- `tools/` - Utility scripts
- `pyproject.toml` - Python project configuration

---

### 2. **install-alpine** (Alpine Installation Generator)
**Link**: `install-alpine` → `/home/abuss/Work/devel/kodos-musl/experiments/install`

#### Overview
Experimental **Lua-based Alpine Linux installation generator**. Generates shell scripts that install Alpine Linux with services, packages, and configuration.

#### Key Features
- **Alpine-specific** (not Arch like the main kodos tool)
- **OpenRC service management** (Alpine's init system, not systemd)
- **Lua generator**: Converts Lua config → shell commands for `chroot` installation
- **Service enablement**: Automatically enables OpenRC services at boot
- **Nine boot-level system services** pre-configured (devfs, proc, sysfs, cgroup, urandom, seedrng, sysctl, networking, hostname)

#### Service Configuration Pattern
```lua
services = {
    ssh = {
        packages = { "openssh" },
        service_name = "sshd",
        settings = { PermitRootLogin = "no" },
    },
}
```

Generates:
```bash
chroot /mnt /bin/sh -c "apk add --quiet openssh"
chroot /mnt /bin/sh -c "rc-update add sshd default"
# Configuration for ssh:
#   PermitRootLogin = no
```

#### Key Differences from Arch
| Aspect | Alpine (OpenRC) | Arch (systemd) |
|--------|---|---|
| Package manager | `apk` | `pacman` |
| Enable service | `rc-update add SERVICE default` | `systemctl enable SERVICE` |
| Service files | `/etc/init.d/` | `/etc/systemd/system/` |
| Runlevels | boot, default, sysinit | targets |

#### File Structure
- `alpine-install-generator.lua` - Main generator
- `configuration.lua` - Example config
- `package-resolver.lua` - Package handling
- `run-generator.lua` - Entry point
- `test-install.sh` - Generated installation commands
- `*.md` files - Comprehensive documentation
  - `README_SERVICES.md` - Service configuration guide
  - `SERVICES_CONFIGURATION_GUIDE.md` - Deep dive
  - `IMPLEMENTATION_SUMMARY.md` - Overview

#### Status
🔧 **Experimental/Research** - Well-documented but appears to be a parallel investigation into Alpine support

---

### 3. **generation-apks** (APK Package Generation)
**Link**: `generation-apks` → `/home/abuss/Work/devel/kodos-musl/experiments/apk-usage`

#### Overview
Research project for **APK package handling** — investigating how to work with Alpine Linux packages programmatically.

#### Key Features
- **APK database initialization**: Creating fresh APK roots
- **Package fetching**: Downloading packages from Alpine repositories
- **Package inspection**: Analyzing dependencies and metadata
- **Local repository testing**: Setting up local APK repos
- **Package extraction**: Unpacking `.apk` files to a store layout

#### Technical Work
- Experimented with 14 different APK commands to resolve database issues
- Successfully resolved APK database initialization
- Struggled with local repository configuration (warnings but not blocking)

#### Key Findings
1. APK database must be initialized with `--initdb` flag on `add` command
2. Repository warnings don't prevent package discovery
3. Extracted packages can be stored in `store/<name>/<version>/files` layout
4. Dependencies (like musl) must be separately installed (not automatic with file extraction)

#### File Structure
- `install-apks.py` - Python package installation tool
- `check-chroot-links.py` - Symlink validation
- `apk` - Alpine APK binary
- `repo/` - Local Alpine repository mirror
- `rootfs/` - Test chroot environment
- `command-report.md` - Detailed APK command investigation log

#### Status
📊 **Experimental/Investigation** - Research into APK ecosystem for potential Alpine integration

---

### 4. **proc-lua-conf** (Lua Configuration Processing)
**Link**: `proc-lua-conf` → `/home/abuss/Work/devel/nkodos`

#### Overview
Utility library for **processing Lua configuration files** — handles deep merging, shape detection, and configuration composition.

#### Key Features
- **Shape detection**: Distinguishes arrays from dictionaries in Lua
- **Deep merge operations**:
  - Array + Array → concatenate
  - Dict + Dict → overlay with recursion
  - Type conflicts → second value wins
- **Configuration composition**: Combining multiple config sources
- **Recursive merging**: Handles nested structures cleanly

#### Example Merge
```lua
deep_merge(
    {server = {host = "localhost", port = 8080}},
    {server = {port = 9090, tls = true}}
)
-- Result: {server = {host = "localhost", port = 9090, tls = true}}
```

#### File Structure
- `demo.lua` - 8 comprehensive test cases
- `demo2.lua` - Additional tests
- `test-*.lua` - Various test configurations
- `install.lua` - Installation script
- `src/` - Source code
- `examples/` - Usage examples

#### Status
✅ **Stable utility** - Core Lua table processing library with comprehensive tests

---

## Architectural Relationships

```
                  kodos-merge (this workspace)
                        |
        ________________|________________
       |               |               |
       v               v               v
    kodos      install-alpine    generation-apks    proc-lua-conf
   (Main CLI)  (Alpine Gen)      (APK Research)     (Lua Utils)
   - Python    - Lua Generator   - APK Tools        - Lua Tables
   - Arch/AUR - Alpine/OpenRC    - Investigation    - Deep Merge
   - SystemD  - Services         - Experiments      - Composition
```

### Data Flow
1. **User writes**: Lua configuration file
2. **kodos (or generator)**: Reads Lua config using `proc-lua-conf` for merging
3. **install-alpine**: Converts config → APK install commands (Alpine-specific)
4. **generation-apks**: Handles APK package operations
5. **Result**: Bootable Alpine Linux system with services enabled

---

## Current Status Summary

| Project | Status | Type | Language |
|---------|--------|------|----------|
| **kodos** | ⚠️ WIP | CLI Tool | Python |
| **install-alpine** | 🔧 Experimental | Generator | Lua |
| **generation-apks** | 📊 Research | Toolkit | Python |
| **proc-lua-conf** | ✅ Stable | Library | Lua |

---

## Key Observations

### Strengths
1. ✅ **Clear separation of concerns**: Each project has a focused responsibility
2. ✅ **Well-documented**: Especially `install-alpine` with comprehensive guides
3. ✅ **Declarative approach**: Lua-based configuration enables reproducibility
4. ✅ **Generational system**: Btrfs snapshots + atomic rollback is powerful
5. ✅ **Multi-distro vision**: Designed to support Arch, Alpine, and Debian eventually

### Areas of Note
1. ⚠️ **Early stage**: All projects are experimental/WIP, not production-ready
2. ⚠️ **Alpine support incomplete**: `install-alpine` is parallel investigation, not integrated into main `kodos`
3. ⚠️ **APK handling uncertain**: Package extraction/storage patterns still being researched
4. 🤔 **Integration unclear**: How/when these Alpine tools integrate into main kodos unclear
5. 📝 **Architecture vs. Documentation**: Design choices documented but implementation patterns still evolving

### Gaps
1. No apparent integration tests between projects
2. No clear merge plan (Alpine into main kodos)
3. APK research incomplete (local repo issues)
4. Limited examples combining all tools

---

## Recommended Next Steps

### For Integration
1. Define how `install-alpine` → main `kodos` transition happens
2. Resolve APK local repository issues in `generation-apks`
3. Add cross-project integration tests
4. Create Alpine installation examples

### For Documentation
1. Add architecture diagram showing data flow
2. Create "getting started" guide combining all tools
3. Document expected Alpine/Arch parity
4. Clarify Debian roadmap

### For Code
1. Move `install-alpine` experiments into main kodos if stable
2. Formalize APK handling in `generation-apks`
3. Add CI/CD tests for generated scripts
4. Create example configs for common scenarios

---

## Summary

This workspace is a **meta-repository linking core KodOS components in various states of development**. The main vision is clear (Lua-declarative Linux distro management), but execution is distributed across multiple experimental projects. The symlinks allow parallel development while keeping components loosely coupled. Key work ahead is integrating Alpine support and solidifying the APK handling before considering any of this production-ready.
