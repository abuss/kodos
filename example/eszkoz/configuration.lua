-- Configuration: eszkoz — MONOLITHIC form, DISTRO-NEUTRAL.
-- Same configuration as system.lua + modules/ + packages/ + home.lua,
-- written in one file (verified by test-equivalence.lua).
-- The distro is declared once (repos.base.type); everything else distro-
-- specific lives in the backend (backends/alpine.lua).

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
    -- "zen", "rt", "cloud"); absent = the distro's default. Which variants
    -- exist per distro: see backends/*.lua kernel_packages (Alpine v3.24:
    -- lts/stable/virt; Arch: default/lts/hardened/zen; deb: default/lts/rt/cloud).
    boot = {
        bootloader = { type = "grub", efi = true, timeout = 10 },
        kernel = {
            -- variant = "lts",
            modules = { "xhci_pci", "ehci_pci", "ohci_pci", "virtio_pci",
                "virtio_blk", "ahci", "usbhid", "sr_mod" },
            arguments = { "iommu=pt" },
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
                    }, -- for hibernation
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
        locale = { default = "en_US.UTF-8" }, -- musl limits locales (backend note)
        serial_console = false, -- true = getty on the kernel console tty
                                -- (VMs via qemu -nographic need this)
    },

    -- ====== SERVICES (plain data; names/packages/units from the backend) ======
    -- Keyed by neutral service name; enable = true required (NixOS-style).
    -- A layer disables with enable = false.
    services = {
        networkmanager = { enable = true },
        mdev = { enable = true }, -- cold-plug of =m modules (see modules/services.lua)
        syslog = { enable = true }, -- /var/log/messages
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
    },

    -- ====== USERS (home layer carried inline) ======
    -- DICT keyed by username: the key is the identity; layers merge natively.
    users = {
        root = {
            name = "Root account",
            group = "root",
            supplementary_groups = { "wheel" },
            home_directory = "/root",
            shell = "/bin/sh",
            no_password = true,
            create_home = false,
        },
        abuss = {
            name = "Antal Buss",
            group = "users",
            supplementary_groups = { "wheel", "audio", "video", "input",
                "lp", "docker", "kvm" },
            home_directory = "/home/abuss",
            shell = "/usr/bin/zsh",
            create_home = true,

            -- ====== USER ENVIRONMENT (home layer) ======
            environment = {
                packages = base("neovim", "helix", "emacs", "zsh", "bash", "git",
                    "lazygit", "node", "python3", "fzf", "ripgrep", "fd",
                    "alacritty", "foot", "syncthing"),

                config = {
                    git = {
                        user = { name = "Antal Buss", email = "antal.buss@gmail.com" },
                        core = { editor = "helix", whitespace = "trailing-space,space-before-tab" },
                        pull = { rebase = true },
                        diff = { algorithm = "histogram" },
                    },
                    zsh = {
                        zshrc = {
                            "# Zsh configuration",
                            "export EDITOR=helix",
                            'export PATH="$HOME/.local/bin:$PATH"',
                            "alias ll='ls -la'",
                            "alias gs='git status'",
                            "alias gd='git diff'",
                        },
                    },
                    bash = {
                        bashrc = {
                            "# Bash configuration",
                            "export EDITOR=helix",
                            'export PATH="$HOME/.local/bin:$PATH"',
                            "alias ll='ls -la'",
                        },
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
                    -- plain data, keyed by neutral name; the backend decides
                    -- mechanism and instance name (syncthing@abuss is a
                    -- systemd-ism; OpenRC has no user services).
                    syncthing = {
                        enable = true,
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
    -- Entries known to be unavailable on Alpine are simply not listed:
    -- vivaldi, brave, megasync, rslsync, glow, tldr, duf, ghostty.
    packages = extend(
        base("gcc", "g++", "cmake", "make", "ninja", "clang",
            "rustup", "go", "zig", "nim", "lua", "lua-language-server"),
        base("ripgrep", "fd", "fzf", "gping", "btop", "lsd",
            "zoxide", "lazygit", "wget", "hyperfine",
            "broot", "tmux", "dua-cli", "dust", "yazi",
            "zellij", "mc", "neovim", "git"),
        base("stow", "htop", "less", "man", "shadow", "sudo"),
        base("qemu", "libvirt", "dnsmasq", "podman",
            "podman-compose", "distrobox"),
        base("alacritty", "foot", "wezterm"),
        base("openssh", "tailscale", "iw"),
        base("gnome", "gnome-tweaks", "networkmanager"),
        base("font-noto", "font-noto-emoji", "font-dejavu",
            "font-liberation", "font-fira-code")
    ),
}
