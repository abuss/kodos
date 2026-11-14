-- Configuration: eszkoz (Alpine) — MODULAR form.
-- system.lua assembles the system layer; modules/ + packages/ mirror the
-- Guix split; home.lua is the per-user environment layer.
-- Equivalent to configuration.lua (same table) — see test-equivalence.lua.
local k = require("kodos")
local apk, service, extend = k.apk, k.service, k.extend

local users = require("modules.users")
local home = require("home")
users.abuss.environment = home

local eszkoz = {
    -- ====== PACKAGE REPOS (the distro is declared ONCE, right here) ======
    -- The type picks the backend: backends/alpine.lua (repo_types.apk).
    repos = {
        base = {
            type = "apk",
            mirror = "https://dl-cdn.alpinelinux.org/alpine/v3.24",
            branches = { "main", "community" },
        },
        -- Extra repos: flatpak apps live in Flathub's namespace, not the
        -- distro's. List them with the flatpak() helper anywhere packages
        -- are listed: packages = { desktop = flatpak("org.gimp.GIMP") }
        -- (inside a user's environment = per-user install scope).
        -- Declaring this repo makes the backend pull in the flatpak tool
        -- package automatically.
        -- flatpak = { type = "flatpak", name = "flathub",
        --     url = "https://dl.flathub.org/repo/flathub.flatpakrepo" },
    },

    -- ====== BOOT (neutral; kernel/bootloader packages from the backend) ======
    -- kernel.variant picks a NEUTRAL variant (e.g. "lts", "hardened",
    -- "zen", "rt", "cloud"); absent = the distro's default.
    boot = {
        bootloader = { type = "grub", efi = true, timeout = 10 },
        kernel = {
            -- variant = "lts",
            modules = { "xhci_pci", "ehci_pci", "ohci_pci", "virtio_pci",
                        "virtio_blk", "ahci", "usbhid", "sr_mod" },
            arguments = { "iommu=pt" },
        },
    },

    -- ====== STORAGE ======
    storage = {
        devices = require("modules.filesystems"),
    },

    -- ====== GENERATIONS (generation-apks storage model) ======
    generations = {
        storage = "store",   -- package store + symlink generations + etc trees
        root = "/kod",       -- /kod/store, /kod/generation, etc_store/etc_default
        keep = 5,            -- generations retained
    },

    -- ====== SYSTEM (identity, locale, timezone, keyboard) ======
    system = {
        host_name = "eszkoz",
        timezone = "America/Edmonton", -- executor adds tzdata
        keyboard_layout = { layout = "us" },
        console_keymap = "us",
        -- musl has limited locale support; en_US.UTF-8 works natively,
        -- anything beyond needs glibc-compat. Keep it minimal.
        locale = { default = "en_US.UTF-8" },
        serial_console = false, -- true = getty on the kernel console tty
    },

    -- ====== SERVICES (modules/services.lua) ======
    services = require("modules.services"),

    -- ====== USERS (modules/users.lua + home.lua as environment) ======
    users = users,

    -- ====== SYSTEM PACKAGES (packages/all-packages.lua) ======
    packages = require("packages.all-packages").all,
}

return eszkoz
