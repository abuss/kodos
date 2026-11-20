-- vm/arch-config.lua — standalone Arch milestone-1 test machine (vm/run-arch.sh).
-- Deliberately NOT built on the eszkoz module set: those layers are
-- Alpine-flavored (openrc units, apk repo names) and this milestone only
-- needs a bootable Arch system. Identity mirrors vm/vm-layer.lua so the two
-- VM rounds share guest credentials and console plumbing.

return {
    repos = { base = { type = "arch",
                       -- community mirror syncing DB-last (DB-listed => file
                       -- present); matches vm/run-arch.sh's $M. geo.mirror's
                       -- edge cache served a stale DB beside fresh files.
                       mirror = "https://mirror.rackspace.com/archlinux",
                       branches = { "core", "extra" } } },

    boot = {
        bootloader = { type = "grub", efi = true, timeout = 5 },
        -- variant absent: backend default = linux + linux-firmware.
        -- qemu -nographic: the log has to arrive on the serial console.
        kernel = { arguments = { "console=ttyS0" } },
    },

    -- Same shape as executor/lua/modules/filesystems.lua, on the nbd-attached
    -- test disk: GPT + ESP + fixed root + separate /home + swap: the same
    -- four-partition shape the bare-metal Arch media lays out, so the VM
    -- round actually exercises home/mount and swap paths.
    storage = { devices = { disk0 = {
        device = "/dev/nbd0",
        type = "gpt",
        efi = true,
        partitions = {
            { name = "boot", size = "512MB", type = "esp",
              filesystem = "vfat", mountpoint = "/boot", check = false },
            { name = "root", size = "32GB", type = "linux",
              filesystem = "ext4", mountpoint = "/", options = "noatime" },
            { name = "home", size = "100%", type = "linux",
              filesystem = "ext4", mountpoint = "/home", options = "noatime" },
            { name = "swap", size = "8GB", type = "linux-swap",
              filesystem = "swap" },
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

    packages = { { repo = "base", name = "sudo" }, { repo = "base", name = "git" },
        -- this world's base meta stopped pulling mkinitcpio (measured: its
        -- %DEPENDS% has none); install_bootloader builds initramfs-<flavor>.img
        -- with it, and mkinitcpio drags mkinitcpio-busybox for the base hook.
        { repo = "base", name = "mkinitcpio" },
        -- Arch split locale data out of glibc: without it locale-gen (the
        -- activate hook) hits missing charmaps and produces no locales.
        { repo = "base", name = "glibc-locales" } },

    files = {
        -- The generation tree makes /etc read-only, so systemd cannot
        -- generate an ID on first boot (and dbus-broker will not start
        -- without one). Ship a fixed test-VM identity instead.
        machine_id = { path = "/etc/machine-id", mode = "0444",
                       content = "0a1b2c3d4e5f60718293a4b5c6d7e8f9\n" },
        -- Explicit recipe: the package-default HOOKS carry no systemd hook
        -- (initramfsSaneArch requires systemd IN the cpio), and autodetect
        -- inspects the build host's loaded modules, not the guest's. Pin the
        -- qemu virtio block stack via MODULES instead. Syntax: bash ARRAYS
        -- (mkinitcpio SOURCES this; string-form MODULES="a b" becomes ONE
        -- bogus module name).
        mkinitcpio = { path = "/etc/mkinitcpio.conf", content = [[
MODULES=(virtio_pci virtio_blk virtio_net)
HOOKS=(base systemd autodetect modconf block filesystems)
]] },
    },
}
