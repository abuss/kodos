-- build/usb/arch-config/arch-machine.lua — bare-metal Arch install config.
-- Standalone (not layered on the Alpine-flavoured eszkoz system.lua): the
-- milestone-1 Arch backend only needs a bootable systemd + GRUB machine.
--
-- Copy this payload to /root/kodos on the booted Alpine live stick, edit
-- storage.devices.disk0.device for the real disk, then run arch-run-install.sh.
local function sibling(rel)
    local self = debug.getinfo(1, "S").source
    self = self:sub(1, 1) == "@" and self:sub(2) or self
    local dir = self:match("^(.*)/[^/]*$") or "."
    local h = io.open(dir .. "/" .. rel)
    if not h then error("arch-machine: cannot read " .. dir .. "/" .. rel) end
    local s = h:read("*a")
    h:close()
    return s
end

return {
    repos = {
        base = {
            type = "arch",
            mirror = "https://mirror.rackspace.com/archlinux",
            branches = { "core", "extra" }
        }
    },

    boot = {
        bootloader = { type = "grub", efi = true, timeout = 5 },
        -- console order is load-bearing: the LAST console= owns /dev/console,
        -- so userspace output and the login prompt land on the screen (bare
        -- metal round 2026-11-20 froze with ttyS0 last — the prompt went to a
        -- serial port this box has nobody watching). ttyS0 stays first so
        -- kernel messages still reach qemu -nographic rounds.
        kernel = { arguments = { "console=ttyS0,115200", "console=tty0" } },
    },

    -- GPT + ESP + whole-remainder ext4 root. The installer rewrites disk0's
    -- device when --device differs from this default.
    storage = {
        devices = {
            disk0 = {
                device = "/dev/sda",
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
                        size = "100%",
                        type = "linux",
                        filesystem = "ext4",
                        mountpoint = "/",
                        options = "noatime"
                    },
                },
            }
        }
    },

    generations = { storage = "store", root = "/kod", keep = 5 },

    system = {
        host_name = "kodos-arch",
        timezone = "America/Edmonton",
        keyboard_layout = { layout = "us" },
        console_keymap = "us",
        locale = { default = "en_US.UTF-8" },
        -- Dual console: tty0 keeps bare-metal VGA output; ttyS0 makes
        -- qemu -nographic boot visible. Arch backend enables the getty.
        serial_console = true,
    },

    services = {
        ssh = { enable = true, permit_root_login = true },
        networkmanager = { enable = true },
    },

    -- Passwords arrive from the installer's 0600 secrets sidecar; never here.
    users = {
        root = {
            name = "Root account",
            group = "root",
            home_directory = "/root",
            shell = "/bin/bash",
            create_home = false,
        },
        abuss = {
            name = "Install user",
            group = "wheel",
            supplementary_groups = {},
            home_directory = "/home/abuss",
            shell = "/bin/bash",
            create_home = true,
        },
    },

    packages = { { repo = "base", name = "sudo" }, { repo = "base", name = "git" },
        -- base no longer pulls mkinitcpio; install_bootloader builds the
        -- initramfs with it. glibc-locales is required by locale-gen.
        { repo = "base", name = "mkinitcpio" },
        { repo = "base", name = "glibc-locales" },
        { repo = "base", name = "mc" }
    },

    files = {
        -- The generation tree makes /etc read-only; ship a fixed identity so
        -- systemd and dbus-broker can start on first boot.
        machine_id = {
            path = "/etc/machine-id",
            mode = "0444",
            content = "0a1b2c3d4e5f60718293a4b5c6d7e8f9\n"
        },
        -- autodetect inspects the BUILD host's loaded modules, not the target
        -- machine's: wrong for bare metal. Drop it so block/filesystems carry
        -- the full driver set; the larger initramfs is deliberate.
        mkinitcpio = {
            path = "/etc/mkinitcpio.conf",
            content = [[
MODULES=()
HOOKS=(base systemd modconf block filesystems)
]]
        },
        -- The installed config.lua calls sibling(); ship the sibling beside it
        -- so the file on the machine still loads, including for day-2 apply.
        config = {
            path = "/etc/kodos/config.lua",
            content = sibling("arch-machine.lua")
        },
        arch_machine = {
            path = "/etc/kodos/arch-machine.lua",
            content = sibling("arch-machine.lua")
        },
    },
}
