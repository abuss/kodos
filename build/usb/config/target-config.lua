-- /etc/kodos/config.lua — the bare-metal box's OWN config (self-management).
-- machine.lua ships this content into the image; afterwards every change is:
--
--     kodos apply --dry-run && kodos apply      (default config /etc/kodos/config.lua)
--
-- It mirrors machine.lua's deltas as literal overlays over the embedded base
-- (same deep_merge semantics the layer stack uses). kodos itself is a static
-- binary in the active generation, not a package from a local repo.
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
        -- COSMIC greeter is the display manager; seatd is its OpenRC
        -- dependency, elogind its seat requirement, dbus the bus both ride.
        seatd = { enable = true },
        elogind = { enable = true },
        dbus = { enable = true },
        ["cosmic-greeter"] = { enable = true },
    },
    packages = {
        -- COSMIC desktop (the system layer no longer ships GNOME).
        -- cosmic-session pulls comp+greeter+apps+xwayland; seatd/elogind
        -- for the greeter's seat chain; mesa-dri-gallium = software GL
        -- (no GPU accel in NanoVM: GBM lands on the simpledrm card);
        -- icons/locales/desktop-file-utils = Alpine COSMIC wiki minimal.
        { repo = "base", name = "cosmic-session" },
        { repo = "base", name = "cosmic-term" },
        { repo = "base", name = "seatd" },
        { repo = "base", name = "elogind" },
        { repo = "base", name = "mesa-dri-gallium" },
        -- cosmic-greeter.pam lists pam_gnome_keyring.so; when the module is
        -- absent PAM flags it faulty and greetd's pam_setcred fails with
        -- MODULE_UNKNOWN -> greeter respawn loop (field round 2026-11-17).
        -- Module only; full gnome-keyring (Secret Service) is opt-in.
        { repo = "base", name = "gnome-keyring-pam" },
        { repo = "base", name = "adwaita-icon-theme" },
        { repo = "base", name = "hicolor-icon-theme" },
        { repo = "base", name = "musl-locales" },
        { repo = "base", name = "desktop-file-utils" },
    },
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
