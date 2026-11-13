-- Partitions for eszkoz (Alpine).
-- ext4 everywhere: the generation store (/kod) uses package dedup +
-- symlinks, so nothing here needs btrfs.
-- ponytail: placeholder sizes, confirm with lsblk before any real install.
local devices = {
    disk0 = {
        device = "/dev/nvme0n1",
        type = "gpt",
        efi = true,
        partitions = {
            { name = "boot", size = "512MB", type = "esp",
              filesystem = "vfat", mountpoint = "/boot", check = false },
            { name = "root", size = "64GB", type = "linux",
              filesystem = "ext4", mountpoint = "/", options = "noatime" },
            { name = "home", size = "100%", type = "linux",
              filesystem = "ext4", mountpoint = "/home", options = "noatime" },
            { name = "swap", size = "34GB", type = "linux-swap",
              filesystem = "swap" }, -- for hibernation
        },
    },
}

return devices
