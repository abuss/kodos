-- User accounts for eszkoz (Alpine).
-- DICT keyed by username (like services): the key is the identity, so
-- config layers merge users natively — no special merge rule needed.
-- abuss's `environment` (home layer) is attached in system.lua.
-- No password hashes here: secrets belong outside the config file
-- (injected by the executor), never committed.
-- Alpine groups: lp replaces Arch lpadmin; wheel/audio/video/input/kvm exist.
local users = {
    root = {
        name = "Root account",
        group = "root",
        supplementary_groups = { "wheel" },
        home_directory = "/root",
        shell = "/bin/sh", -- Alpine default (ash)
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
    },
}

return users
