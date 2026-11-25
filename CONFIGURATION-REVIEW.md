# Configuration Review: example/eszkoz/configuration.lua

## Status: ❌ INVALID — 7 Issues Found

The configuration has **schema violations** and **architectural mismatches** with the kodos IR spec. A corrected version is provided in `configuration-fixed.lua`.

---

## Issues Detailed

### 🔴 CRITICAL: Schema Violations

#### Issue 1: Invalid repo type `"pacman"` (Line 51)
**Severity:** CRITICAL  
**Location:** `repos.base.type`  
**Problem:**
```lua
type = "pacman",  -- ❌ NOT in schema enum
```
**Schema requires:**
```lua
type = "apk" | "arch" | "deb"  -- enum only
```
**Fix:** Change `"pacman"` to `"arch"`

---

#### Issue 2: Invalid locale structure (Line 134)
**Severity:** CRITICAL  
**Location:** `system.locale`  
**Problem:**
```lua
locale = {
    default = "en_CA.UTF-8",
    extra = { "en_US.UTF-8", "en_GB.UTF-8" }  -- ❌ 'extra' not in schema
}
```
**Schema requires:**
```lua
locale = { default = "string" }  -- ONLY default field allowed
```
**Why:** Locale generation is per-backend, not IR-level. Arch's glibc backend generates ANY locale via `locale-gen`; Alpine's musl is restricted to pre-built list.

**Fix:** Remove `extra` key; rely on backend's `locale_packages` and `locale-gen`.

---

#### Issue 3: Undefined top-level `desktop` section (Lines 174-215)
**Severity:** CRITICAL  
**Location:** `desktop` table  
**Problem:**
```lua
desktop = {
    display_manager = "cosmic-greeter",
    desktops = { ... }
}
```
**Schema:** No `desktop` key in IR spec.

**Why:** Desktop manager, display server, and theme config are **distro-specific details**, not distro-neutral IR. These belong in the **backend** (backends/arch.lua, etc.), not in user-facing config.

**Fix:** Remove entire `desktop` section. Desktop packages go in `packages` list as tagged items.

---

### 🟠 ARCHITECTURAL: AUR/Flatpak Not Supported at System Level

#### Issue 4: AUR repo declared as top-level repo (Lines 54-61)
**Severity:** ARCHITECTURAL  
**Location:** `repos.aur`  
**Problem:**
```lua
aur = {
    type = "aur",
    helper = "yay",
}
```
**Why:** 
- Schema only recognizes `repos.base` (required) + `repositories` (local file:/// repos)
- AUR is **not a repository type** in the IR spec
- `type` enum only allows: `"apk"`, `"arch"`, `"deb"`

**How AUR actually works in kodos:**
- System-level: NOT SUPPORTED — package repos must be distro-official (apk, pacman, apt)
- User-level: **SUPPORTED** — users can list AUR packages in `users.<name>.environment.packages` with `{repo="aur", name="package"}` tags
  - Backend sees the tag and handles installation mechanism (e.g., yay for Arch)
  - Works because it's per-user; home environment is backend-specific

**Fix:** 
- Remove AUR repo declaration (lines 54-61)
- Keep user-level AUR packages (lines 192-198) — these are VALID

---

#### Issue 5: Flatpak repo declared with unsupported fields (Lines 58-61)
**Severity:** ARCHITECTURAL  
**Location:** `repos.flatpak`  
**Problem:**
```lua
flatpak = {
    type = "flatpak",
    name = "flathub",  -- ❌ 'name' not in schema; should be 'url'
}
```
**Schema for optional `repos.flatpak`:**
```lua
flatpak = {
    type = "flatpak",  -- ✓ required
    name = "string",   -- ✓ flathub remote name
    url = "string",    -- ✓ remote URL (required by spec)
}
```
**Missing field:** `url` is required by schema but not provided.

**However:** Flatpak apps in Kodos are **user-level only** (like AUR). Declare system-wide packages in `packages`, tag with `{repo="flatpak", name="org.app.id"}`.

**Fix:** Remove flatpak repo. If per-user flatpak apps needed, add to `users.abuss.environment.packages` with `flatpak()` helper.

---

### 🟡 SCHEMA: Service-level package overrides (Lines 160, 162)

#### Issue 6: Printing service with redundant packages (Line 160)
**Severity:** DESIGN  
**Location:** `services.printing.packages`  
**Problem:**
```lua
printing = {
    enable = true,
    packages = base("cups", "gutenprint"),  -- ❌ overrides backend
}
```
**Why:** Backend already defines `printing` → `{packages: ["cups"]}` (Arch). User-provided packages override, but `gutenprint` is a driver add-on.

**Better approach:**
```lua
printing = { enable = true }  -- backend handles cups
-- gutenprint goes in top-level packages if needed
packages = extend(
    base("gutenprint"),  -- optional printer driver
    ...
)
```

**Fix:** Remove from services; add `gutenprint` to top-level packages if desired.

---

#### Issue 7: Audio service with redundant packages (Line 162)
**Severity:** DESIGN  
**Location:** `services.audio.packages`  
**Problem:**
```lua
audio = {
    enable = true,
    packages = base("pipewire", "pipewire-alsa", "pipewire-pulse", "pipewire-jack", "wireplumber"),
}
```
**Why:** Audio is **per-user** (systemd --user service), and backend already provides baseline (pipewire + pipewire-pulse + wireplumber). Explicit packages duplicate.

**Better approach:**
```lua
audio = { enable = true }  -- backend handles pipewire stack
```

**Fix:** Remove explicit packages; use backend defaults.

---

## Corrected Version

A fixed version **without schema violations** is provided: **`configuration-fixed.lua`**

### Key Changes:
1. ✅ `repos.base.type = "arch"` (was `"pacman"`)
2. ✅ Removed `locale.extra` (Arch handles all locales via glibc)
3. ✅ Removed entire `desktop` section (backend handles display_manager)
4. ✅ Removed `repos.aur` and `repos.flatpak` declarations
5. ✅ Cleaned up service entries (no package overrides)
6. ✅ AUR/VSCode/Quickemu/etc in user environment only
7. ✅ GNOME/COSMIC packages in top-level `packages` list

### Schema Validation: ✅ PASSES

The fixed config conforms to:
- `/home/abuss/Work/devel/kodos-merge-2/executor/lua/validate.lua` (all required fields, no extra keys)
- Arch backend: `/home/abuss/Work/devel/kodos-merge-2/executor/lua/backends/arch.lua` (service names, package maps)
- IR contract: `/home/abuss/Work/devel/kodos-merge-2/example/eszkoz/IR.md`

---

## How to Use

```bash
# Validate corrected config
cd /home/abuss/Work/devel/kodos-merge-2
./executor/kodos exec example/eszkoz/configuration-fixed.lua --dry-run

# Or use the fixed version as the new example
cp example/eszkoz/configuration-fixed.lua example/eszkoz/configuration.lua
```

---

## Design Notes

**Why AUR/Desktop are user-level, not system-level:**

| Aspect | System (`packages` / `services`) | User (`users.X.environment`) |
|--------|----------------------------------|------------------------------|
| **Scope** | Distro-neutral IR | Per-user, backend-specific |
| **AUR** | ❌ Not supported (non-official repo) | ✅ Arch-specific, via home env |
| **Flatpak** | ❌ Not supported (needs user namespaces) | ✅ Per-user apps, via home env |
| **Desktop** | ❌ Not supported (distro-specific details) | ✅ Backend handles via dconf/gsettings |

This design keeps the IR **distro-agnostic**; Arch-specific power-user features (AUR, flatpak, desktop themes) live in the **user environment** layer where the backend has full control.

---

## Files

| File | Status |
|------|--------|
| `example/eszkoz/configuration.lua` | ❌ Invalid (7 issues) |
| `example/eszkoz/configuration-fixed.lua` | ✅ Valid (schema-compliant) |
| `example/eszkoz/IR.md` | ✅ Reference spec |
| `executor/lua/validate.lua` | ✅ Schema definition |
| `executor/lua/backends/arch.lua` | ✅ Arch backend |
