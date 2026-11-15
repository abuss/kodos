-- /etc/kodos/config.lua — the VM's OWN config: the machine managing itself.
-- vm/vm-layer.lua ships this file into the image at bootstrap (write_file),
-- because `kodos apply` reads its config from DISK and the kodos binary embeds
-- only the TOOLCHAIN (system.lua + modules/ + packages/ + backends/ come out
-- of the binary's preload set), never the machine's config.
--
-- The build host's layer files (vm/vm-layer.lua, example/eszkoz/layers/*) are
-- NOT on this machine, so their content is mirrored below as literal overlays
-- merged over the embedded base with the SAME semantics the layer stack uses
-- (resolve.deep_merge: arrays append, dicts overlay). Edit this file, then:
--
--     kodos apply --dry-run && kodos apply     (config.lua in /etc/kodos = the default)
local resolve = require("resolve") -- embedded: the config-layer merge rules

local conf = require("system") -- embedded eszkoz base profile (users/home/etc)

-- ---- vm/vm-layer.lua material (throwaway test machine) ----
conf = resolve.deep_merge(conf, {
    system = { host_name = "kodos-vm", serial_console = true },
    boot = {
        kernel = {
            variant = "virt", -- smaller + firmware-none
            arguments = { "console=ttyS0" }, -- qemu -nographic reads the serial line
        },
    },
    -- qemu attaches the qcow2 with if=virtio, so on the GUEST the disk is
    -- /dev/vda (install-time it was /dev/nbdX seen from the host; fstab mounts
    -- by UUID, so only this identity matters now).
    storage = { devices = { disk0 = { device = "/dev/vda" } } },
    -- linux-virt is built with "# CONFIG_BT is not set": bluetoothd can never
    -- start on this VM; the desktop config keeps it for real hardware.
    services = {
        bluetooth = { enable = false },
        -- paste-run convenience ONLY: vm/run.sh drives this guest over ssh as
        -- root (sshpass, throwaway creds). A real machine keeps the base
        -- profile's permit_root_login = false.
        ssh = { permit_root_login = true },
    },
    -- the kodos repo, shipped into this image by copy_tree at bootstrap. src
    -- ABSOLUTE = this machine's own /kod/repo: the copy self-skips and
    -- apk/fetch read the repo straight off the local disk.
    repositories = { kodos = { url = "file:///kod/repo", src = "/kod/repo" } },
    packages = { { repo = "kodos", name = "kodos" } },
})

-- ---- example/eszkoz/layers/machine.lua material ----
conf = resolve.deep_merge(conf, {
    boot = {
        bootloader = { timeout = 3 }, -- this box boots fast
        kernel = { arguments = { "quiet" } },
    },
    services = {
        printing = { enable = false }, -- no printer on this box
        audio = { packages = { { repo = "base", name = "pipewire-pulse" } } },
        tlp = { enable = true, packages = { { repo = "base", name = "tlp" } } },
        networkmanager = {
            connections = {
                wired = { type = "ethernet" },
                home = { type = "wifi", ssid = "homecloud",
                         psk = { secret = "wifi.home.psk" } }, -- /etc/kodos/secrets.json
            },
        },
    },
    packages = { { repo = "base", name = "htop" } },
    users = { abuss = { shell = "/bin/bash" } },
})

-- ---- example/eszkoz/layers/user.lua material ----
conf = resolve.deep_merge(conf, {
    users = {
        abuss = {
            environment = {
                packages = { { repo = "base", name = "bat" },
                             { repo = "base", name = "dust" } },
                config = { git = { alias = { st = "status" } } },
            },
        },
    },
})

-- ---- self-management: the repo's fixture package, added LIVE ----
-- Bootstrap (vm-layer) installs only kodos itself; `dummy` appearing HERE is
-- the proof that a package can be added to a running machine by editing its
-- own config and applying it. Delete this line, re-apply: generation N+2.
conf = resolve.deep_merge(conf, {
    packages = { { repo = "kodos", name = "dummy" } },
})

return conf
