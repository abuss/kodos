-- Home environment for 'abuss' — DISTRO-NEUTRAL.
-- Attached as users.abuss.environment in system.lua.
local k = require("kodos")
local base = k.base

return {
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
                { host = "git.homecloud.lan",
                  options = { HostName = "git.homecloud.lan", User = "abuss",
                              IdentityFile = "~/.ssh/id_rsa",
                              StrictHostKeyChecking = "accept-new" } },
                { host = "*",
                  options = { AddKeysToAgent = "yes", ControlMaster = "auto",
                              ControlPath = "~/.ssh/control-%h-%p-%r" } },
            },
        },
    },

    services = {
        -- plain data, keyed by neutral name; the backend decides mechanism
        -- and instance name (syncthing@abuss is a systemd-ism; OpenRC has
        -- no user services).
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
}
