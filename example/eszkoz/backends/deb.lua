-- backends/deb.lua — per-distro backend for repo type "deb".
-- Same contract as alpine.lua / arch.lua (see PORTABILITY.md). Package
-- names verified against Debian trixie main + contrib + non-free-firmware.
-- Debian is the heaviest renamer: its naming conventions diverge the most
-- from the neutral vocabulary (network-manager, golang, fd-find, ...).
-- Like Arch: systemd init, units inside packages, glibc locale-gen (any
-- locale compilable -> no locales_supported list to validate).
local M = {}

M.name = "deb"
M.repo_types = { deb = true }
M.init = "systemd" -- systemctl enable <unit>

-- Debian has no "base" meta; the bootstrap set is composed here.
M.base_packages = { "apt", "systemd", "tzdata" }
-- Kernel variants: trixie's default series (6.12) already IS the LTS
-- kernel and has no separate alias, so lts maps to the default; rt/cloud
-- have stable aliases. firmware-linux lives in non-free-firmware (needs
-- that repo enabled).
M.kernel_packages = {
    default = { "linux-image-amd64", "firmware-linux" },
    lts = { "linux-image-amd64", "firmware-linux" },
    rt = { "linux-image-rt-amd64", "firmware-linux" },
    cloud = { "linux-image-cloud-amd64", "firmware-linux" },
}
M.bootloader_packages = {
    grub = { "grub-efi-amd64", "efibootmgr" },
}

-- Installed when the config declares repos.flatpak (app ids are Flathub names)
M.flatpak_packages = { "flatpak" }

-- Canonical config name -> Debian name. false = not in trixie
-- main/contrib/non-free-firmware (drop, warn). Absent = identity.
M.map = {
    fd = "fd-find",
    go = "golang",
    lua = "lua5.4",
    man = "man-db",
    networkmanager = "network-manager",
    ninja = "ninja-build",
    node = "nodejs",
    openssh = "openssh-server", -- pulls openssh-client
    qemu = "qemu-system-x86",
    shadow = "passwd",          -- useradd/usermod live in passwd on Debian
    dust = "du-dust",
    libvirt = "libvirt-daemon-system",
    ["font-noto"] = "fonts-noto",
    ["font-noto-emoji"] = "fonts-noto-color-emoji",
    ["font-dejavu"] = "fonts-dejavu",
    ["font-fira-code"] = "fonts-firacode",
    ["font-liberation"] = "fonts-liberation",
    -- not packaged in trixie (main+contrib+n-f):
    ["dua-cli"] = false,
    helix = false,              -- user impact: also in the home layer
    ["lua-language-server"] = false,
    nim = false,
    tailscale = false,
    ["tailscale-openrc"] = false, -- no packaged unit files either
    wezterm = false,
    yazi = false,
    zellij = false,
    zig = false,
}

-- Units: note ssh.service (NOT sshd -- that is an Arch/RHEL-ism) and
-- avahi's package name being avahi-daemon (unit = package name here).
M.services = {
    networkmanager = {
        packages = { "network-manager" },
        service_name = "NetworkManager",
    },
    -- systemd-udevd coldplugs =m modules unconditionally: nothing to do.
    mdev = { service_name = false },
    -- journald is part of systemd and always logging: nothing to do.
    syslog = { service_name = false },
    ssh = {
        packages = { "openssh-server" },
        service_name = "ssh",
        settings_file = "/etc/ssh/sshd_config",
    },
    printing = {
        packages = { "cups" },
        service_name = "cups",
    },
    bluetooth = {
        packages = { "bluez" },
        service_name = "bluetooth",
    },
    avahi = {
        packages = { "avahi-daemon" }, -- the PACKAGE is named avahi-daemon
        service_name = "avahi-daemon",
    },
    audio = {
        packages = { "pipewire", "pipewire-pulse", "wireplumber" },
        service_name = false, -- per-user session stack
    },
    input = {
        packages = { "xserver-xorg-input-libinput" },
        service_name = false,
    },
    -- no tailscale entry: trixie has no tailscale at all, so the config's
    -- hatch packages all map to false and the service degrades to
    -- "dropped with warning" -- correct behavior, verified by the checker.
}

M.home_services = {
    syncthing = {
        packages = { "syncthing" },
        mechanism = "systemd-user", -- trixie ships syncthing@.service template
    },
}

-- glibc: locale-gen compiles any locale (en_CA.UTF-8 included); the
-- `locales` package carries /etc/locale.gen + locale-gen.
M.locale_packages = { "locales" }

return M
