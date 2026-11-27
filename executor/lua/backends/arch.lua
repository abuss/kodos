-- backends/arch.lua — per-distro backend for repo type "arch".
-- Same contract as alpine.lua (see PORTABILITY.md). Package names verified
-- against the current core+extra sync DB.
-- Arch differences from Alpine: units ship INSIDE the main package (no
-- -openrc-style unit subpackages), systemd enables user services natively,
-- and glibc can compile any locale via locale-gen.
local M = {}

M.name = "arch"
M.repo_types = { arch = true }
M.repo_branches = { "core", "extra" } -- default sync repos when repos.base.branches is absent
M.init = "systemd" -- enable mechanism: systemctl enable <unit>

M.base_packages = { "base", "tzdata", "ca-certificates",
    "mkinitcpio" } -- base meta includes systemd; base no longer ships mkinitcpio (2026-11)
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
    -- Display managers (Arch-specific, mapped for IR generation)
    gdm = "gnome-shell",        -- gnome-shell provides gdm
    sddm = "sddm",
    lightdm = "lightdm",
    ["cosmic-greeter"] = "cosmic-greeter",
}

-- Backend-owned desktop session sets: enabling a known desktop pulls the
-- whole set (greeter included, so the DM unit always
-- has a provider). extra_packages stays additive on top; unknown names
-- fall through to hand-listed packages (generate.lua warns).
M.sessions = {
    -- KNOWN WORLD LIMITATION (odin 2026-11-27): this repo set ships NO
    -- gdk-pixbuf loader modules except webp-pixbuf-loader, so GdkPixbuf
    -- decodes no png/svg: GNOME's Shell/app-grid icons are blank on ANY
    -- install of this world (gtk4/glycin/Qt/COSMIC paths unaffected).
    -- Nothing to pin; report upstream if GNOME icons matter here.
    gnome = { "epiphany", "evolution-data-server", "gdm",
              "gnome-backgrounds", "gnome-calculator", "gnome-control-center",
              "gnome-online-accounts", "gnome-session", "gnome-settings-daemon",
              "gnome-shell", "gnome-system-monitor", "gnome-terminal",
              "gnome-text-editor", "gvfs", "nautilus",
              "xdg-desktop-portal-gnome" },
    plasma = { "dolphin", "kde-cli-tools", "konsole", "plasma-desktop", "plasma-nm",
               "plasma-pa", "plasma-keyboard", "plasma-workspace", "polkit-kde-agent",
               "sddm", "xdg-desktop-portal-kde" },
    cosmic = { "cosmic-greeter", "cosmic-osk", "cosmic-session" },

    -- X11 desktops pair with lightdm+gtk-greeter, wayland compositors
    -- with greetd+tui greeter: display_manager must name a greeter the
    -- chosen set provides (or one mapped in M.map). xorg-xinit is in
    -- the X sets so bare WMs can also start from a tty. cage is a
    -- single-app kiosk: no greeter by design. All names verified
    -- against the live index (2026-11-26).
    xfce = { "lightdm", "lightdm-gtk-greeter", "thunar", "xfce4-panel",
             "xfce4-power-manager", "xfce4-session", "xfce4-settings",
             "xfce4-terminal", "xfce4-whiskermenu-plugin", "xfdesktop",
             "xfwm4", "xorg-server", "xorg-xinit" },
    mate = { "caja", "lightdm", "lightdm-gtk-greeter", "marco",
             "mate-control-center", "mate-panel", "mate-session-manager",
             "mate-settings-daemon", "mate-terminal", "xorg-server",
             "xorg-xinit" },
    cinnamon = { "cinnamon", "cinnamon-session", "cinnamon-settings-daemon",
                 "gdm", "gnome-terminal", "nemo", "xdg-desktop-portal-gnome" },
    budgie = { "budgie-desktop", "budgie-extras", "gdm",
               "gnome-control-center", "nautilus" },
    lxqt = { "lxqt-panel", "lxqt-runner", "lxqt-session", "pcmanfm-qt",
             "qterminal", "sddm", "xorg-server", "xorg-xinit" },
    i3 = { "dmenu", "i3-wm", "i3lock", "i3status", "lightdm",
           "lightdm-gtk-greeter", "xterm", "xorg-server", "xorg-xinit" },
    openbox = { "lightdm", "lightdm-gtk-greeter", "openbox", "xterm",
                "xorg-server", "xorg-xinit" },
    enlightenment = { "enlightenment", "lightdm", "lightdm-gtk-greeter",
                      "terminology", "xorg-server", "xorg-xinit" },
    sway = { "foot", "foot-terminfo", "fuzzel", "greetd", "greetd-tuigreet",
             "mako", "sway", "swaybg", "swaylock", "xdg-desktop-portal-wlr" },
    hyprland = { "foot", "foot-terminfo", "fuzzel", "greetd",
                 "greetd-tuigreet", "hyprland", "hyprlock", "hyprpaper",
                 "xdg-desktop-portal-wlr" },
    wayfire = { "foot", "foot-terminfo", "greetd", "greetd-tuigreet", "mako",
                "wayfire", "wf-recorder", "wofi", "xdg-desktop-portal-wlr" },
    labwc = { "foot", "foot-terminfo", "fuzzel", "greetd", "greetd-tuigreet",
              "labwc", "xdg-desktop-portal-wlr" },
    river = { "foot", "foot-terminfo", "fuzzel", "greetd", "greetd-tuigreet",
              "river", "xdg-desktop-portal-wlr" },
    cage = { "cage" },
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

-- VT login prompt: systemd 258+ ships no autovt@ template, and Arch applies
-- its presets through pacman hooks the executor bypasses — so nothing ever
-- instantiates getty@tty1 and the screen ends boot with no prompt (bare
-- metal round 2026-11-20). Enable it explicitly instead.
M.vt_console_unit = "getty@tty1"

return M
