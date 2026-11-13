-- layers/machine.lua — MACHINE layer overlay for eszkoz.
-- Merged over the base config (system.lua), under layers/user.lua.
-- Demonstrates the layering semantics of the dialect:
--   * NixOS-style disable: services.printing = { enable = false }
--   * scalar override:     bootloader.timeout 10 -> 3
--   * array append:        kernel arguments, service packages, categories
--   * user record keyed merge: same name -> extends, never duplicates
-- All added packages are canonical names verified on Alpine v3.24.
return {
    boot = {
        bootloader = { timeout = 3 },           -- this box boots fast
        kernel = { arguments = { "quiet" } },   -- appended to iommu=pt
    },

    services = {
        printing = { enable = false },          -- no printer on this laptop
        audio = { packages = { { repo = "base", name = "pipewire-pulse" } } },
        -- new service introduced by the layer (escape hatch: no backend entry)
        tlp = { enable = true, packages = { { repo = "base", name = "tlp" } } },
        -- networks are site/machine specific -> layer material
        networkmanager = {
            connections = {
                wired = { type = "ethernet" },
                home = { type = "wifi", ssid = "homecloud",
                         psk = { secret = "wifi.home.psk" } },
            },
        },
    },

    -- appended to the flat package list (htop already in base: dedup demo)
    packages = { { repo = "base", name = "htop" } },

    -- keyed into the existing abuss record (dict merge by username)
    users = {
        abuss = { shell = "/bin/bash" },
    },
}
