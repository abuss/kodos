-- test-merge.lua — prove the layering semantics of the dialect.
-- base (system.lua) <- machine layer <- user layer. Run: lua test-merge.lua
local resolve = require("resolve")

local conf = resolve.load({ "system.lua", "layers/machine.lua", "layers/user.lua" })

local function find(list, want)
    for _, v in ipairs(list) do
        if type(v) == "table" and v.name == want then return v end
        if v == want then return v end
    end
end

-- 1. NixOS-style disable: machine layer kills a base service
assert(conf.services.printing.enable == false,
       "printing must be disabled by the machine layer")
-- base settings survive a partial overlay (dict merge, not replace)
assert(conf.services.printing.dns_sd == true,
       "printing.dns_sd from base must survive the enable=false overlay")

-- 2. scalar override, sibling keys intact
assert(conf.boot.bootloader.timeout == 3, "bootloader.timeout must be overridden")
assert(conf.boot.bootloader.efi == true, "bootloader.efi must survive the dict merge")

-- 3. array append
local args = conf.boot.kernel.arguments
assert(#args == 2 and args[1] == "iommu=pt" and args[2] == "quiet",
       "kernel arguments must concatenate base + layer")

-- 4. service packages additive across layers; new service appears
local audiopkgs = conf.services.audio.packages
assert(#audiopkgs == 2, "audio packages: base + machine layer")
assert(find(audiopkgs, "pipewire-jack") and find(audiopkgs, "pipewire-pulse"),
       "both layers' audio packages must be present")
assert(conf.services.tlp.enable == true, "layer-introduced service must appear")

-- 5. users dict keyed by username: layer merge extends, never duplicates
local n = 0
for _ in pairs(conf.users) do n = n + 1 end
assert(n == 2, "users must not duplicate; got " .. n)
local root, abuss = conf.users.root, conf.users.abuss
assert(root and abuss, "both users survive the merge")
assert(abuss.shell == "/bin/bash", "machine layer overrides abuss shell")
assert(root.shell == "/bin/sh", "root untouched by the layers")
assert(root.name == "Root account", "root fields intact")

-- 6. home layer (environment) extended by the user layer, keyed deep merge
local envpkgs = abuss.environment.packages
assert(find(envpkgs, "neovim") and find(envpkgs, "bat") and find(envpkgs, "dust"),
       "home packages: base home + user layer additions")
assert(abuss.environment.config.git.alias.st == "status", "git alias from user layer")
assert(abuss.environment.config.git.user.name == "Antal Buss",
       "git user from home layer survives the deep merge")

-- 7. nothing else moved: distro inference + repos untouched
assert(conf.repos.base.type == "apk", "repos must survive untouched")
assert(conf.system.host_name == "eszkoz", "system section untouched")

-- 8. the merged config must still be fully valid: hand it to the service
--    planner and check the disable took effect on the resolved plan
local backend = resolve.backend_for(conf)
local plan = resolve.service_plan(conf, backend)
local units, names = {}, {}
for _, s in ipairs(plan) do
    names[s.name] = s
    if s.unit then units[s.unit] = true end
end
assert(not names.printing, "disabled service must vanish from the plan")
assert(not units.cupsd, "cupsd unit must not be enabled")
assert(units.sshd and units.networkmanager, "enabled units must be in the plan")
assert(names.tlp and names.tlp.unit == "tlp", "escape-hatch unit = service key")
assert(names.tailscale and names.tailscale.unit == "tailscale",
       "tailscale escape hatch: unit = key, packages from config")
-- audio: backend says service_name=false -> packages only, no unit
assert(names.audio and not names.audio.unit, "audio: packages only on Alpine")
-- audio packages: backend baseline (distro names) + config additions (mapped)
local ap = names.audio.packages
assert(find(ap, "pipewire") and find(ap, "wireplumber")
       and find(ap, "pipewire-jack") and find(ap, "pipewire-pulse"),
       "audio packages = backend baseline + additive config lists")

-- 9. home services resolved with the backend's mechanism
local hsp = resolve.home_service_plan(conf, backend)
assert(#hsp == 1 and hsp[1].user == "abuss" and hsp[1].name == "syncthing"
       and hsp[1].mechanism == "user-daemon", "syncthing home service resolved")

print("PASS: layer merge + service planning (base + machine + user)")
