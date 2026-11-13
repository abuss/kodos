-- Package categories for eszkoz — DISTRO-NEUTRAL canonical names.
-- The backend (backends/alpine.lua) maps canonical -> distro names and
-- drops (with a warning) anything unavailable on the target distro.
-- Entries known to be Arch-only are simply not listed: vivaldi, brave,
-- megasync, rslsync, glow, tldr, duf, ghostty (no Alpine v3.22 package).
local k = require("kodos")
local base, extend = k.base, k.extend

local all = {
    dev_tools = base("gcc", "g++", "cmake", "make", "ninja", "clang",
                     "rustup", "go", "zig", "nim", "lua", "lua-language-server"),
    cli_tools = base("ripgrep", "fd", "fzf", "gping", "btop", "lsd",
                     "zoxide", "lazygit", "wget", "hyperfine",
                     "broot", "tmux", "dua-cli", "dust", "yazi",
                     "zellij", "mc", "neovim", "git"),
    system_tools = base("stow", "htop", "less", "man", "shadow", "sudo"),
    virtualization = base("qemu", "libvirt", "dnsmasq", "podman",
                          "podman-compose", "distrobox"),
    terminals = base("alacritty", "foot", "wezterm"),
    network = base("openssh", "tailscale", "iw"),
    desktop = base("gnome", "gnome-tweaks", "networkmanager"),
    fonts = base("font-noto", "font-noto-emoji", "font-dejavu",
                 "font-liberation", "font-fira-code"),
}

all.all = extend(all.dev_tools, all.cli_tools, all.system_tools,
                 all.virtualization, all.terminals, all.network,
                 all.desktop, all.fonts)

return all
