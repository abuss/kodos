-- /etc/kodos/config.lua — the bare-metal box's OWN config (self-management).
-- machine.lua ships this content into the image; afterwards every change is:
--
--     kodos apply --dry-run && kodos apply      (default config /etc/kodos/config.lua)
--
-- It mirrors machine.lua's deltas as literal overlays over the embedded base
-- (same deep_merge semantics the layer stack uses), with the repo pointed at
-- the machine's OWN /kod/repo (copy_tree self-skips; fetch reads it off disk).
-- The vm/guest-config/config.lua shape is what the VM rounds proved.
local resolve = require("resolve") -- embedded: the config-layer merge rules

local conf = require("system") -- embedded eszkoz base profile

conf = resolve.deep_merge(conf, {
    system = { host_name = "kodos-box" },
    boot = { bootloader = { timeout = 3 }, kernel = { arguments = { "quiet" } } },
    -- identity on the installed machine (fstab mounts by UUID, so only this
    -- name matters from here on): EDIT to the disk lsblk shows.
    storage = { devices = { disk0 = { device = "/dev/sda" } } },
    services = {
        printing = { enable = false },
        networkmanager = { connections = { wired = { type = "ethernet" } } },
    },
    repositories = { kodos = { url = "file:///kod/repo", src = "/kod/repo" } },
    packages = { { repo = "kodos", name = "kodos" } },
    -- lean initramfs: Alpine's mkinitfs 'kms' feature sweeps GPU modules +
    -- ALL their firmware blobs (~100MB+) into the initramfs; NanoVM's UEFI
    -- stub failed to start a 180MB image. GPU comes up under openrc — only
    -- disk/net drivers are needed before the root mount.
    files = {
        mkinitfs = { path = "/etc/mkinitfs/mkinitfs.conf",
                     content = [[features="ata base cdrom ext4 keymap mmc nvme raid scsi usb virtio"]] },
    },
})

return conf
