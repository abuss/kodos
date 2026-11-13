-- vm-layer.lua — test-machine overlay: the same eszkoz config pointed at a
-- qcow2 disk attached via nbd (vm/run.sh). Demonstrates layering doing real
-- work: identity, kernel variant and disk are all that differ.
return {
    system = { host_name = "kodos-vm" },
    boot = {
        kernel = {
            variant = "virt", -- smaller + firmware-none
            -- qemu -nographic: the log has to arrive on the serial console
            -- (arrays append on merge: base arguments + this one)
            arguments = { "console=ttyS0" },
        },
    },
    storage = { devices = { disk0 = { device = "/dev/nbd0" } } },
}
