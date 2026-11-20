-- backends/arch.lua — per-distro backend for repo type "arch".
-- Same contract as alpine.lua (see PORTABILITY.md). Package names verified
-- against the current core+extra sync DB.
-- Arch differences from Alpine: units ship INSIDE the main package (no
-- -openrc-style unit subpackages), systemd enables user services natively,
-- and glibc can compile any locale via locale-gen.
local M = {}

M.name = "arch"
M.repo_types = { arch = true }
M.init = "systemd" -- enable mechanism: systemctl enable <unit>

M.base_packages = { "base", "tzdata", "ca-certificates" } -- base meta includes systemd
-- Kernel variants (boot.kernel.variant): Arch has the richest menu.
M.kernel_packages = {
    default = { "linux", "linux-firmware" },
    lts = { "linux-lts", "linux-firmware" },
    hardened = { "linux-hardened", "linux-firmware" },
    zen = { "linux-zen", "linux-firmware" },
}
M.bootloader_packages = {
    grub = { "grub", "efibootmgr" },
    ["systemd-boot"] = { "efibootmgr" }, -- bootctl ships with systemd (base)
}

-- Installed when the config declares repos.flatpak (app ids are Flathub names)
M.flatpak_packages = { "flatpak" }

-- Canonical config name -> Arch name. false = unavailable (drop, warn).
M.map = {
    ["g++"] = "gcc",     -- gcc provides g++ (real provider-semantics TODO)
    gnome = "gnome-shell", -- "gnome" is a group on Arch, not a package
    man = "man-db",
    node = "nodejs",
    python3 = "python",
    qemu = "qemu-desktop",
    ["font-noto"] = "noto-fonts",
    ["font-noto-emoji"] = "noto-fonts-emoji",
    ["font-dejavu"] = "ttf-dejavu",
    ["font-liberation"] = "ttf-liberation",
    ["font-fira-code"] = "ttf-fira-code",
    ["tailscale-openrc"] = false, -- Arch's tailscale ships its own units
}

-- Service registry (see alpine.lua for the Alpine -openrc quirk; Arch has
-- no unit subpackages). Units: systemd unit names, .service implied.
M.services = {
    networkmanager = {
        packages = { "networkmanager" },
        service_name = "NetworkManager",
    },
    -- systemd-udevd coldplugs + tags links unconditionally: nothing to do.
    udev = { service_name = false },
    -- journald is part of systemd and always logging: nothing to do.
    syslog = { service_name = false },
    ssh = {
        packages = { "openssh" },
        service_name = "sshd",
        settings_file = "/etc/ssh/sshd_config",
    },
    printing = {
        packages = { "cups" }, -- dns_sd via avahi (enabled as its own service)
        service_name = "cups", -- was org.cups.cupsd before 2019
    },
    bluetooth = {
        packages = { "bluez" }, -- bluez-utils merged into bluez
        service_name = "bluetooth",
    },
    avahi = {
        packages = { "avahi" },
        service_name = "avahi-daemon",
    },
    audio = {
        packages = { "pipewire", "pipewire-pulse", "wireplumber" },
        service_name = false, -- per-user on systemd too
    },
    input = {
        packages = { "xf86-input-libinput" },
        service_name = false,
    },
    -- Not an escape-hatch case here: Arch's unit is tailscaled, NOT the
    -- config key "tailscale" (which is why Alpine works via the hatch --
    -- its init script is literally named tailscale). Config packages
    -- (tailscale, tailscale-openrc->dropped-by-map) layer on top.
    tailscale = {
        packages = { "tailscale" },
        service_name = "tailscaled",
    },
}

M.home_services = {
    syncthing = {
        packages = { "syncthing" },
        mechanism = "systemd-user", -- unit: syncthing@abuss.service (native)
    },
}

-- glibc ships locale-gen + i18n sources in the glibc package itself: any
-- locale (incl. en_CA.UTF-8) can be compiled; nothing to pre-validate.
M.locale_packages = {}

-- Serial console: systemd instantiates serial-getty@ from the template unit
-- that systemd ships; the executor enables it as a getty.target.wants link
-- (Alpine instead ships a full /etc/inittab — see generate.lua's
-- serial_console branch: backend.inittab OR backend.serial_console_unit).
M.serial_console_unit = "serial-getty@ttyS0"

return M
