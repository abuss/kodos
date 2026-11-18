-- vm/arch-config.lua — standalone Arch milestone-1 test machine (vm/run-arch.sh).
-- Deliberately NOT built on the eszkoz module set: those layers are
-- Alpine-flavored (openrc units, apk repo names) and this milestone only
-- needs a bootable Arch system. Identity mirrors vm/vm-layer.lua so the two
-- VM rounds share guest credentials and console plumbing.

return {
    repos = { base = { type = "arch",
                       mirror = "https://geo.mirror.pkgbuild.com",
                       branches = { "core", "extra" } } },

    boot = {
        bootloader = { type = "grub", efi = true, timeout = 5 },
        -- variant absent: backend default = linux + linux-firmware.
        -- qemu -nographic: the log has to arrive on the serial console.
        kernel = { arguments = { "console=ttyS0" } },
    },

    -- Same shape as executor/lua/modules/filesystems.lua, on the nbd-attached
    -- test disk: GPT + ESP, whole remainder is the ext4 root. No home/swap:
    -- throwaway VM, and the generation store lives in root.
    storage = { devices = { disk0 = {
        device = "/dev/nbd0",
        type = "gpt",
        efi = true,
        partitions = {
            { name = "boot", size = "512MB", type = "esp",
              filesystem = "vfat", mountpoint = "/boot", check = false },
            { name = "root", size = "100%", type = "linux",
              filesystem = "ext4", mountpoint = "/", options = "noatime" },
        },
    } } },

    generations = { storage = "store", root = "/kod", keep = 5 },

    system = {
        host_name = "kodos-vm",
        timezone = "America/Edmonton",
        keyboard_layout = { layout = "us" },
        console_keymap = "us",
        locale = { default = "en_US.UTF-8" },
        serial_console = true, -- serial-getty@ttyS0 enable via arch backend
    },

    -- networkmanager: a real wants-symlink service (proves the systemd enable
    -- path); ssh: proves the sshd_config settings path. NetworkManager takes
    -- the qemu user-net NIC by default on a fresh arch boot.
    services = {
        ssh = { enable = true, permit_root_login = true },
        networkmanager = { enable = true },
    },

    -- Passwords never live here: the executor injects users.<login>.hashed_password
    -- from the 0600 secrets sidecar (same keys as the Alpine vm round).
    users = {
        root = {
            name = "Root account", group = "root", home_directory = "/root",
            shell = "/bin/bash", create_home = false,
        },
        abuss = {
            name = "Test User", group = "wheel", supplementary_groups = {},
            home_directory = "/home/abuss", -- createUser passes -d verbatim
            shell = "/bin/bash", create_home = true,
        },
    },

    packages = { { repo = "base", name = "sudo" }, { repo = "base", name = "git" } },
}
