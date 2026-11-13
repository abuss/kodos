-- backends/alpine.lua — per-distro backend for repo type "apk".
-- The config (configuration.lua / system.lua) is distro-neutral: it names
-- services by neutral name (plain data) and packages by canonical name. Everything distro-specific
-- lives HERE. This file is the spec for the generator's backend registry --
-- the compiled executor reimplements the action generation, the data model
-- is this one.
-- Package names verified against Alpine v3.24 main+community.
local M = {}

M.name = "alpine"
M.repo_types = { apk = true } -- repos.base.type picks this backend
M.init = "openrc" -- enable mechanism: rc-update add <unit> <runlevel>

-- Packages the base system always needs, never listed in user config.
M.base_packages = { "alpine-base", "openrc", "dbus",
    -- bootability facts the executor relies on: mkinitfs runs from the
    -- kernel's .post-install hook (initramfs is required: Alpine kernels
    -- build virtio/ext4 as modules), and tzdata provides the zoneinfo
    -- tree that configure_timezone links into /etc/localtime.
    "mkinitfs", "tzdata" }

-- Kernel: keyed by NEUTRAL variant (boot.kernel.variant selects; absent =
-- "default"). Alpine flavors verified on v3.24 x86_64: lts (main, the
-- default), virt (main, VM guests), stable (community, was linux-edge
-- <=3.22). linux-openpax and the rpi/asahi families are NOT in 3.24
-- x86_64 main/community. Firmware: linux-firmware is the big meta;
-- pick linux-firmware-none or the split linux-firmware-* packages when
-- smaller is wanted (backend default keeps the generous meta).
M.kernel_packages = {
    default = { "linux-lts", "linux-firmware" },
    lts = { "linux-lts", "linux-firmware" },
    stable = { "linux-stable", "linux-firmware" },
    virt = { "linux-virt", "linux-firmware-none" }, -- VM guests: virtio is
    -- built into linux-virt; no firmware blobs needed at all
}
M.bootloader_packages = {
    grub = { "grub", "grub-efi", "efibootmgr" },
}

-- Installed when the config declares repos.flatpak; the app ids themselves
-- are Flathub names ({repo="flatpak"} tags), no backend mapping needed.
M.flatpak_packages = { "flatpak" }

-- Canonical config name -> Alpine name. false = unavailable (drop, warn).
-- Absent entries pass through unchanged.
M.map = {
    clang = "clang20",
    ninja = "ninja-build",
    node = "nodejs",
    man = "mandoc",
    lua = "lua5.4",
    ["dua-cli"] = "dua",
    ["font-fira-code"] = "font-fira-code-nerd",
    -- vivaldi = false, glow = false, tldr = false, duf = false (no v3.24 pkg)
}

-- Locale support on Alpine (verified v3.24 + Alpine wiki Locale page, 2026-05):
-- musl ships only C/C.UTF-8; musl-locales supplies a fixed compiled set --
-- no en_CA. Translations are separate: the `lang` meta-package pulls
-- *-lang subpackages for every installed package. LANG lives in
-- /etc/profile.d/20locale.sh; the console needs unicode="YES" in /etc/rc.conf.
M.locale_packages = { "musl-locales", "musl-locales-lang" }
M.locales_supported = {
    "C.UTF-8", "cs_CZ.UTF-8", "de_CH.UTF-8", "de_DE.UTF-8", "en_GB.UTF-8",
    "en_US.UTF-8", "es_ES.UTF-8", "fi_FI.UTF-8", "fr_FR.UTF-8", "it_IT.UTF-8",
    "nb_NO.UTF-8", "nl_NL.UTF-8", "pt_BR.UTF-8", "pt_PT.UTF-8", "ru_RU.UTF-8",
    "sr_RS.UTF-8", "sv_SE.UTF-8",
}
M.rc_conf = { unicode = "YES" } -- needs a unicode console font too

-- Services: keyed by the config's neutral service name -> OpenRC reality.
-- enable/runlevel/packages are the generator's job; settings writers
-- (e.g. sshd_config) map config keys to /etc files. Alpine quirk: init
-- scripts are separate -openrc subpackages.
-- Names MISSING here are still usable from the config via the escape
-- hatch: config provides packages; enable name = the service key (unless
-- overridden by service_name; false = packages only).
M.services = {
    networkmanager = {
        packages = { "networkmanager", "networkmanager-openrc", "ethtool", "iw" },
        service_name = "networkmanager", runlevel = "boot",
    },
    ssh = {
        packages = { "openssh", "openssh-server-common-openrc" },
        service_name = "sshd", runlevel = "default",
        settings_file = "/etc/ssh/sshd_config",
    },
    printing = {
        packages = { "cups", "cups-openrc", "avahi-compat-libdns_sd" },
        service_name = "cupsd", runlevel = "default",
    },
    bluetooth = {
        packages = { "bluez", "bluez-openrc" },
        service_name = "bluetooth", runlevel = "default",
    },
    avahi = {
        packages = { "avahi", "avahi-openrc" },
        service_name = "avahi-daemon", runlevel = "default",
    },
    audio = {
        packages = { "pipewire", "pipewire-pulse", "wireplumber" },
        service_name = false, -- per-user on Alpine, no system OpenRC service
    },
    input = {
        packages = { "xf86-input-libinput" },
        service_name = false, -- X driver, packages only
    },
}

-- Home services: OpenRC has no per-user services -- backend decides the
-- mechanism and the instance name (syncthing@abuss is a systemd-ism).
M.home_services = {
    syncthing = {
        packages = { "syncthing" },
        mechanism = "user-daemon", -- TODO: s6/supervise mapping in executor
    },
}

return M
