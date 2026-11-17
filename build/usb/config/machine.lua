-- build/usb/config/machine.lua — bare-metal BOOTSTRAP layer for the install USB.
-- run-install.sh feeds it to the embedded toolchain:
--     kodos lua generate.lua system.lua <this file>  ->  actions.json
-- so it only needs the deltas from the embedded eszkoz base (system.lua), the
-- same layering the VM rounds proved (vm/vm-layer.lua is the sibling example).
--
-- EDIT before installing: host_name and the ONE device line below — run
-- `cat /proc/partitions` on the booted stick (no lsblk on the live until
-- run-install.sh installs util-linux) and point it at the real disk. NEVER the USB
-- stick you boot from (run-install.sh double-checks and refuses).

-- Read a file NEXT TO this layer (the lua runner chdirs, so io.open must find
-- this file's own dir via debug.getinfo; gopher-lua chunknames may lack Lua's
-- leading '@', so strip it only when present).
local function sibling(rel)
    local self = debug.getinfo(1, "S").source
    self = self:sub(1, 1) == "@" and self:sub(2) or self
    local dir = self:match("^(.*)/[^/]*$") or "."
    local h = io.open(dir .. "/" .. rel)
    if not h then error("machine: cannot read " .. dir .. "/" .. rel) end
    local s = h:read("*a")
    h:close()
    return s
end

return {
    system = { host_name = "kodos-box" },
    boot = { bootloader = { timeout = 3 }, kernel = { arguments = { "quiet" } } },

    -- The embedded modules/filesystems.lua carries the whole gpt + esp +
    -- root/home/swap layout (with placeholder sizes: adjust if the real disk
    -- disagrees); dict merge overlays ONLY the device here.
    storage = { devices = { disk0 = { device = "/dev/sda" } } },

    -- No kernel override: the Alpine backend's DEFAULT variant is
    -- linux-lts + linux-firmware, which is what real hardware wants
    -- (the VM layer is what picks the smaller "virt").

    services = {
        printing = { enable = false },
        -- wired only — the install assumption (network on the target).
        -- Add wifi when needed:
        --   [ssid] = { type = "wifi", ssid = "...", psk = { secret = "wifi.x.psk" } }
        -- and put the psk into the payload's config/secrets.json.
        networkmanager = { connections = { wired = { type = "ethernet" } } },
        -- COSMIC greeter is the display manager (GNOME/GDM dropped from
        -- the system layer 2026-11-17): cosmic-greeter-daemon's OpenRC
        -- script "need seatd"; the greeter itself requires logind.
        seatd = { enable = true },
        elogind = { enable = true },
        dbus = { enable = true },
        ["cosmic-greeter"] = { enable = true },
    },

    -- The USB payload repo: copy_tree ships it INTO the machine at /kod/repo;
    -- run-install.sh rewrites this path to wherever the payload actually is
    -- (the same actions.json sed trick vm/run.sh proved).
    repositories = { kodos = { url = "file:///kod/repo", src = "/usb/repo" } },
    packages = { { repo = "kodos", name = "kodos" },
        -- must mirror target-config.lua (the machine's own config) — the
        -- services above enable the greeter, so its packages must be in
        -- the install closure too (see target-config.lua for the why).
        { repo = "base", name = "cosmic-session" },
        { repo = "base", name = "cosmic-term" },
        { repo = "base", name = "seatd" },
        { repo = "base", name = "elogind" },
        { repo = "base", name = "mesa-dri-gallium" },
        { repo = "base", name = "adwaita-icon-theme" },
        { repo = "base", name = "hicolor-icon-theme" },
        { repo = "base", name = "musl-locales" },
        { repo = "base", name = "desktop-file-utils" },
    },

    -- the machine's OWN self-management config, landed at bootstrap (binary
    -- embeds the toolchain, never the machine config — same as the VM):
    files = {
        config = { path = "/etc/kodos/config.lua",
                   content = sibling("target-config.lua") },
        -- NanoVM field round 2026-11-16: Alpine's mkinitfs 'kms' feature
        -- sweeps the whole GPU driver tree plus every module's firmware
        -- blobs (amdgpu/i915: ~10MB each) into the initramfs — 180MB, which
        -- NanoVM's minimal UEFI stub refused to start (grub start_image
        -- LOAD_ERROR; its 108MB gen-0 image still booted). Kodos needs only
        -- disk/net drivers early; drop kms.
        mkinitfs = { path = "/etc/mkinitfs/mkinitfs.conf",
                     content = [[features="ata base cdrom ext4 keymap mmc nvme raid scsi usb virtio"]] },
    },
}
