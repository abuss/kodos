-- Configuration: eszkoz — MONOLITHIC form, DISTRO-NEUTRAL.
-- Same configuration as system.lua + modules/ + packages/ + home.lua,
-- written in one file (verified by test-equivalence.lua).
-- The distro is declared once (repos.base.type); everything else distro-
-- specific lives in the backend (backends/alpine.lua, backends/arch.lua).
-- Updated to match actual bare-metal Arch install with COSMIC/GNOME desktop
-- and full developer + media workstation packages.

-- ==================================================================
-- LOCAL HELPERS (self-contained; no require())
-- ==================================================================

local function extend(...)
    local out = {}
    for _, t in ipairs({ ... }) do
        for _, v in ipairs(t) do out[#out + 1] = v end
    end
    return out
end

-- tagged package list: { repo = "base", name = "openssh" }
local function base(...)
    local out = {}
    for _, n in ipairs({ ... }) do out[#out + 1] = { repo = "base", name = n } end
    return out
end

-- user-level aur package list: { repo = "aur", name = "package-name" }
-- NOTE: AUR packages only work in user environment (not system-level)
local function aur(...)
    local out = {}
    for _, n in ipairs({ ... }) do out[#out + 1] = { repo = "aur", name = n } end
    return out
end

-- flatpak app list: { repo = "flatpak", name = "org.gimp.GIMP" }
local function flatpak(...)
    local out = {}
    for _, n in ipairs({ ... }) do out[#out + 1] = { repo = "flatpak", name = n } end
    return out
end

-- ==================================================================
-- OPERATING SYSTEM CONFIGURATION (System Layer)
-- ==================================================================

return {
    -- ====== PACKAGE REPOS (the distro is declared ONCE, right here) ======
    -- The type picks the backend: backends/arch.lua (repo_types.arch).
    repos = {
        base = {
            type = "arch",
            mirror = "https://mirror.rackspace.com/archlinux",
        },
    },

    -- ====== BOOT (neutral; kernel/bootloader packages from the backend) ======
    -- kernel.variant picks a NEUTRAL variant (e.g. "lts", "hardened",
    -- "zen", "rt", "cloud"); absent = the distro's default. Which variants
    -- exist per distro: see backends/*.lua kernel_packages (Alpine v3.24:
    -- lts/stable/virt; Arch: default/lts/hardened/zen; deb: default/lts/rt/cloud).
    boot = {
        bootloader = { type = "systemd-boot", efi = true, timeout = 10 },
        kernel = {
            modules = { "xhci_pci", "ohci_pci", "ehci_pci", "virtio_pci", "ahci", "usbhid", "virtio_blk" },
        },
    },

    -- ====== STORAGE (ext4; generation store needs no btrfs) ======
    storage = {
        devices = {
            disk0 = {
                device = "/dev/nvme0n1",
                type = "gpt",
                efi = true,
                partitions = {
                    {
                        name = "boot",
                        size = "512MB",
                        type = "esp",
                        filesystem = "vfat",
                        mountpoint = "/boot",
                        check = false
                    },
                    {
                        name = "root",
                        size = "64GB",
                        type = "linux",
                        filesystem = "ext4",
                        mountpoint = "/",
                        options = "noatime"
                    },
                    {
                        name = "home",
                        size = "100%",
                        type = "linux",
                        filesystem = "ext4",
                        mountpoint = "/home",
                        options = "noatime"
                    },
                    {
                        name = "swap",
                        size = "34GB",
                        type = "linux-swap",
                        filesystem = "swap"
                    },
                },
            },
        },
    },

    -- ====== GENERATIONS (generation-apks storage model) ======
    generations = {
        storage = "store", -- package store + symlink generations + etc trees
        root = "/kod",     -- /kod/store, /kod/generation, etc_store/etc_default
        keep = 5,          -- generations retained
    },

    -- ====== SYSTEM (identity, locale, timezone, keyboard) ======
    system = {
        host_name = "eszkoz",
        timezone = "America/Edmonton",
        keyboard_layout = { layout = "us" },
        console_keymap = "us",
        locale = { default = "en_CA.UTF-8" },
        serial_console = false,
    },

    -- ====== SERVICES (plain data; names/packages/units from the backend) ======
    -- Keyed by neutral service name; enable = true required (NixOS-style).
    -- A layer disables with enable = false.
    services = {
        networkmanager = { enable = true },
        udev = { enable = true },
        syslog = { enable = true },
        ssh = {
            enable = true,
            permit_root_login = false,
            password_authentication = true,
            port = 22,
        },
        printing = { enable = true },
        bluetooth = { enable = true },
        avahi = { enable = true },
        tailscale = { enable = true },
        audio = { enable = true },
        input = { enable = true },
        fwupd = { enable = true },
    },

    -- ====== USERS (home layer carried inline) ======
    -- DICT keyed by username: the key is the identity; layers merge natively.
    users = {
        root = {
            name = "Root account",
            group = "root",
            supplementary_groups = { "wheel" },
            home_directory = "/root",
            shell = "/bin/bash",
            no_password = true,
            create_home = false,
        },
        abuss = {
            name = "Antal Buss",
            group = "users",
            supplementary_groups = { "wheel", "audio", "video", "input", "lp", "docker", "kvm" },
            home_directory = "/home/abuss",
            shell = "/usr/bin/zsh",
            create_home = true,

            -- ====== USER ENVIRONMENT (home layer) ======
            environment = {
                packages = extend(
                    base("neovim", "helix", "emacs", "zsh", "bash", "git", "lazygit",
                        "node", "python3", "fzf", "ripgrep", "fd", "ghostty", "fish",
                        "starship", "stow", "htop"),
                    -- AUR packages (user-level only; system-level AUR not in IR spec)
                    aur("visual-studio-code-bin", "gnome-shell-extension-dash-to-dock",
                        "gnome-shell-extension-blur-my-shell", "gnome-shell-extension-arc-menu-git",
                        "gnome-shell-extension-gsconnect", "quickemu", "megasync-bin", "uxplay")
                ),

                config = {
                    git = {
                        user = { name = "Antal Buss", email = "antal.buss@gmail.com" },
                        core = { editor = "helix", whitespace = "trailing-space,space-before-tab" },
                        pull = { rebase = true },
                        diff = { algorithm = "histogram" },
                    },
                    zsh = {
                        enable = true,
                    },
                    fish = {
                        enable = true,
                    },
                    neovim = {
                        enable = true,
                    },
                    helix = {
                        enable = true,
                    },
                    emacs = {
                        enable = true,
                    },
                    starship = {
                        enable = true,
                    },
                    ssh = {
                        hosts = {
                            {
                                host = "git.homecloud.lan",
                                options = {
                                    HostName = "git.homecloud.lan",
                                    User = "abuss",
                                    IdentityFile = "~/.ssh/id_rsa",
                                    StrictHostKeyChecking = "accept-new"
                                }
                            },
                            {
                                host = "*",
                                options = {
                                    AddKeysToAgent = "yes",
                                    ControlMaster = "auto",
                                    ControlPath = "~/.ssh/control-%h-%p-%r"
                                }
                            },
                        },
                    },
                },

                services = {
                    syncthing = {
                        enable = false,
                        settings = {
                            logflags = { "database", "folder", "model" },
                            gui_address = "127.0.0.1:8384",
                        },
                    },
                },

                dotfiles = {
                    source_dir = "~/.dotfiles",
                    deploy_tool = "stow",
                    repo_url = "http://git.homecloud.lan/abuss/dotconfig.git",
                },
            },
        },
    },

    -- ====== SYSTEM PACKAGES (canonical names; backend maps/drops) ======
    -- Development toolchain + CLI utilities + virtualization + media + desktop
    -- NOTE: GNOME, COSMIC, and other desktop packages go here; backend handles
    -- display_manager, theme, dconf config (distro-specific details).
    packages = extend(
        -- Development tools
        base("gcc", "g++", "cmake", "make", "ninja", "clang", "rustup", "go", "zig", "lua"),
        
        -- CLI utilities
        base("ripgrep", "fd", "fzf", "btop", "lsd", "zoxide", "lazygit", "wget",
            "broot", "tmux", "dua-cli", "dust", "yazi", "zellij", "mc", "neovim", "git",
            "stow", "htop", "less", "man", "sudo"),
        
        -- Virtualization & containers
        base("qemu-desktop", "libvirt", "dnsmasq", "podman", "podman-compose", "podman-docker", "distrobox", "spice-gtk", "remmina"),
        
        -- Terminals & editors
        base("ghostty", "alacritty", "foot", "wezterm"),
        
        -- Networking & system
        base("openssh", "tailscale", "iw", "networkmanager", "power-profiles-daemon"),
        
        -- Fonts
        base("ttf-firacode-nerd", "ttf-nerd-fonts-symbols", "ttf-nerd-fonts-symbols-common",
            "ttf-sourcecodepro-nerd", "ttf-fira-sans", "ttf-fira-code", "ttf-liberation",
            "noto-fonts-emoji", "adobe-source-serif-fonts", "ttf-ubuntu-font-family"),
        
        -- Browsers & internet
        base("firefox", "vivaldi", "vivaldi-ffmpeg-codecs"),
        
        -- Media & document tools
        base("papers"),
        
        -- Desktop environments (distro-specific package names; backend handles)
        -- GNOME
        base("gnome-shell", "gnome-tweaks", "gnome-connections"),
        
        -- COSMIC (cosmic-session = the login session + wayland session file;
        -- cosmic-desktop is not an Arch package)
        base("cosmic-session", "cosmic-greeter")
    ),
}
