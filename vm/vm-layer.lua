-- vm-layer.lua — test-machine overlay: the same eszkoz config pointed at a
-- qcow2 disk attached via nbd (vm/run.sh). Demonstrates layering doing real
-- work: identity, kernel variant and disk are all that differ.
return {
    system = { host_name = "kodos-vm", serial_console = true },
    boot = {
        kernel = {
            variant = "virt", -- smaller + firmware-none
            -- qemu -nographic: the log has to arrive on the serial console
            -- (arrays append on merge: base arguments + this one)
            arguments = { "console=ttyS0" },
        },
    },
    storage = { devices = { disk0 = { device = "/dev/nbd0" } } },
    -- linux-virt is built with "# CONFIG_BT is not set": bluetoothd can
    -- never start on this VM ("Failed to access management interface").
    -- The desktop config enables bluetooth for real hardware; the
    -- throwaway VM test machine opts out. (Layering doing real work.)
    services = { bluetooth = { enable = false } },
}
