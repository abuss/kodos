-- vm-layer.lua — test-machine overlay: the same eszkoz config pointed at a
-- qcow2 disk attached via nbd (vm/run.sh). Demonstrates layering doing real
-- work: identity, kernel variant and disk are all that differ.

-- Read a file that sits NEXT TO this layer (generate.lua runs with a different
-- cwd: host lua from executor/lua, the kodos binary from wherever it lives, so
-- debug.getinfo gives the exact dir when it can and the two plausible cwds
-- cover the rest). The content is captured into the stream, so image and repo
-- cannot drift apart.
local function sibling(rel)
    local self = debug.getinfo(1, "S").source:sub(2)
    local dir = self:match("^(.*)/[^/]*$") or "."
    for _, p in ipairs({ dir .. "/" .. rel, "../../vm/" .. rel, "vm/" .. rel }) do
        local h = io.open(p)
        if h then
            local s = h:read("*a")
            h:close()
            return s
        end
    end
    error("vm-layer: cannot read " .. rel .. " (tried the layer's own dir and vm/)")
end

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
    services = { bluetooth = { enable = false },
                 -- paste rounds drive the guest over ssh as root (vm/run.sh
                 -- guest); throwaway test creds only, see vm/run.sh
                 ssh = { permit_root_login = true } },
    repositories = { kodos = { url = "file:///kod/repo", src = "repo" } },
    packages = { { repo = "kodos", name = "kodos" } },
    -- ====== the guest's self-management material, shipped at bootstrap ======
    -- The kodos binary embeds the Lua toolchain but NOT the machine's config:
    -- `kodos apply` on the installed box needs /etc/kodos/config.lua on disk
    -- (and reads its secrets sidecar from the same directory). `dummy` is NOT
    -- in this stream on purpose — the guest config adds it, which is what
    -- makes the first in-guest apply a live package ADD.
    files = {
        config = { path = "/etc/kodos/config.lua",
                   content = sibling("guest-config/config.lua") },
        -- test creds, already public in this repo (vm/run.sh prints them);
        -- 0600 because that is the executor's secrets-store contract
        secrets = { path = "/etc/kodos/secrets.json", mode = "0600", content = [[{
  "wifi.home.psk": "vm-test-not-a-real-psk",
  "users.root.hashed_password": "$6$YtyfdNihbjP9f9/f$x95/dkgSHm0aPwdPb16Cu0LgAxymhm6o2oJvjMlVEdaWcxvtGqbKV9E9TDrg7FiEnvZvXfgnpVowrDEXJo8re1",
  "users.abuss.hashed_password": "$6$UP1hGpzF7KJsXbSo$LIN0khBa/8SVACzXyUzKmwp7HEpBoDqtQMEVJjj3iurRvRyU4xoMymENk/dGC9SIu2i5lS0oumMruZZ5VRLTV0"
}
]] },
    },
}
