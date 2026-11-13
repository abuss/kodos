-- System services for eszkoz — DISTRO-NEUTRAL, plain data (no helpers).
-- Keyed by neutral service name; enable = true required (NixOS-style).
-- A layer disables with enable = false. The backend (backends/alpine.lua)
-- maps each name to packages, unit name, runlevel, and settings file.
local base = require("kodos").base

local services = {
    networkmanager = { enable = true },
    ssh = {
        enable = true,
        permit_root_login = false,
        password_authentication = true,
        port = 22,
    },
    printing = {
        enable = true,
        dns_sd = true, -- printer sharing via avahi
    },
    bluetooth = { enable = true },
    avahi = { enable = true },
    -- Escape hatch: names with no backend entry need config-provided
    -- packages; the unit to enable is the service key (override with
    -- service_name; false = packages only). Unit/portability is now the
    -- user's responsibility -- that is the price of the hatch.
    tailscale = {
        enable = true,
        packages = base("tailscale", "tailscale-openrc"), -- verified: tailscale-openrc ships /etc/init.d/tailscale
    },
    audio = {
        enable = true,
        -- packages in a service entry are ADDITIVE: merged with the
        -- backend's baseline list (here: pipewire, pipewire-pulse,
        -- wireplumber). Canonical names, mapped/dropped by the backend.
        packages = base("pipewire-jack"),
    },
    input = { enable = true },
}

return services
