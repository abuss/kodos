-- test-validate.lua — the schema must accept the real configs (base and
-- layered) and catch seeded typos/type errors. Run: lua test-validate.lua
local resolve = require("resolve")
local validate = require("validate")

local function must_pass(files, label)
    local errs = validate.validate(resolve.load(files))
    if #errs > 0 then
        for _, e in ipairs(errs) do print("unexpected: " .. e) end
        print("FAIL: " .. label .. " must validate clean")
        os.exit(1)
    end
end

-- 1. the real configs, both forms, layered
must_pass({ "system.lua" }, "modular base")
must_pass({ "../../example/eszkoz/configuration.lua" }, "monolithic")
must_pass({ "system.lua", "../../example/eszkoz/layers/machine.lua", "../../example/eszkoz/layers/user.lua" }, "layered")

-- helper: validate base + scratch layer, assert error substrings appear
local function scratch_layer(name, body)
    local f = assert(io.open(name, "w"))
    f:write(body)
    f:close()
    local errs = validate.validate(resolve.load({ "system.lua", name }))
    os.remove(name)
    local function has(sub)
        for _, e in ipairs(errs) do
            if e:find(sub, 1, true) then return true end
        end
        return false, table.concat(errs, " | ")
    end
    return has
end

-- 2. typo'd section, wrong types, untagged package, repo enum
local has = scratch_layer("_scratch_v1.lua", [[
return {
    serives = { ssh = { enable = true } },
    boot = { bootloader = { timeout = "ten" } },
    services = { ssh = { enable = "yes" } },
    packages = { "git" },
    repos = { flatpak = { type = "apk", name = "x", url = "y" } },
}
]])
assert(has("$.serives: unknown key (typo?)"), "top-level typo not caught")
assert(has("$.boot.bootloader.timeout: expected number, got string"),
       "timeout type not caught")
assert(has("$.services.ssh.enable: expected bool, got string"),
       "enable type not caught")
assert(has("expected tagged package"), "untagged package not caught")
assert(has("$.repos.flatpak.type: must be one of: flatpak"),
       "flatpak repo enum not caught")

-- 3. missing required fields
has = scratch_layer("_scratch_v2.lua", [[
return {
    services = { novnc = { packages = { { repo = "base", name = "novnc" } } } },
    users = { bob = { group = "users" } },
    storage = { devices = { disk1 = { partitions = {
        { name = "p1", size = "1G", type = "linux" } } } } },
}
]])
assert(has("$.services.novnc.enable: missing required field"),
       "service without enable not caught")
assert(has("$.users.bob.name: missing required field"),
       "user without name not caught")
assert(has("$.storage.devices.disk1.device: missing required field"),
       "new disk without device not caught")
assert(has("$.storage.devices.disk1.partitions[1].filesystem: missing required field"),
       "partition without filesystem not caught")

-- 4. service settings must be scalar; environment section is closed
has = scratch_layer("_scratch_v3.lua", [[
return {
    services = { ssh = { deep = { nested = 1 } } },
    users = { abuss = { environment = { configg = {} } } },
}
]])
assert(has("$.services.ssh.deep: service settings values must be scalar"),
       "nested service settings not caught")
assert(has("$.users.abuss.environment.configg: unknown key (typo?)"),
       "environment typo not caught")

-- 4b. networking connections: wifi needs a psk SECRET REFERENCE, a literal
--     psk must fail (raw secrets are inexpressible), enum enforced
has = scratch_layer("_scratch_v5.lua", [[
return {
    services = { networkmanager = { connections = {
        cabin = { type = "wifi", ssid = "cabin" },
        leak = { type = "wifi", ssid = "x", psk = "hunter2" },
        bogus = { type = "fiber" },
    } } },
}
]])
assert(has("connections.cabin: wifi needs psk"), "wifi without psk not caught")
assert(has("connections.leak.psk: expected dict, got string"),
       "literal psk not caught")
assert(has("connections.bogus.type: must be one of: wifi, ethernet"),
       "connection type enum not caught")

-- 5. valid: layer service overlay without enable (inherited from base) passes
has = scratch_layer("_scratch_v4.lua", [[
return { services = { audio = { packages = { { repo = "base", name = "htop" } } } } }
]])
local ok, why = has("NO-SUBSTR")
assert(ok == false, "should have no errors, got: " .. tostring(why))

-- 6. repositories: valid file:// entry passes; wrong type / unknown key caught
has = scratch_layer("_scratch_v6.lua", [[
return { repositories = { mylocal = { url = "file:///kod/repo" } } }
]])
local ok6, why6 = has("NO-SUBSTR")
assert(ok6 == false, "repositories with valid file:// url must pass, got: " .. tostring(why6))

has = scratch_layer("_scratch_v7.lua", [[
return { repositories = { mylocal = { url = 42 } } }
]])
assert(has("$.repositories.mylocal.url: expected string"), "repositories url non-string not caught")

has = scratch_layer("_scratch_v7b.lua", [[
return { repositories = { mylocal = { url = "https://example.com" } } }
]])
assert(has("$.repositories.mylocal: url must start with file:///"), "https:// url not rejected")

has = scratch_layer("_scratch_v7c.lua", [[
return { repositories = { mylocal = { url = "file:/abs" } } }
]])
assert(has("$.repositories.mylocal: url must start with file:///"), "file:/abs url not rejected")

has = scratch_layer("_scratch_v8.lua", [[
return { repositories = { mylocal = { url = "file:///x", extra = "bad" } } }
]])
assert(has("$.repositories.mylocal.extra: unknown key"), "repositories unknown key not caught")

has = scratch_layer("_scratch_v9.lua", [[
return { repositories = { mylocal = { url = "file:///x" } },
         packages = { { repo = "typo", name = "foo" } } }
]])
assert(has("unknown repo 'typo'"), "tagged package with unknown repo not caught")

-- files entries (the machine's own /etc files, Task 13): write_file paths are
-- target-root-absolute, and an unparsable mode would abort a live stream
has = scratch_layer("_scratch_v10.lua", [[
return { files = {
    cfg = { path = "etc/kodos/config.lua", content = "return {}" },
    sec = { path = "/etc/kodos/secrets.json", content = "{}", mode = "600rw" },
} }
]])
assert(has("$.files.cfg: path must be absolute"), "relative files path not rejected")
assert(has('$.files.sec: mode must be an octal string'), "non-octal files mode not rejected")

-- Test: locale.extra as optional list
has = scratch_layer("_scratch_v11.lua", [[
return {
    repos = { base = { type = "apk", mirror = "https://dl-cdn.alpinelinux.org/alpine/v3.24", branches = {} } },
    system = {
        host_name = "test",
        locale = {
            default = "en_US.UTF-8",
            extra = { "en_GB.UTF-8", "en_CA.UTF-8" }
        }
    },
    boot = { bootloader = { type = "grub", efi = true, timeout = 10 },
        kernel = {} },
    storage = { devices = { disk0 = { device = "/dev/sda", type = "gpt", efi = true,
        partitions = { { name = "boot", size = "512MB", type = "esp",
            filesystem = "vfat", mountpoint = "/boot", check = false } } } } },
    generations = { storage = "store", root = "/kod" },
    users = {},
    packages = {}
}
]])
-- Should now validate without error (no extra field errors)
assert(not has("extra: unknown key"), "locale.extra should now be a valid field")

-- Test: desktop section with display_manager and desktops
has = scratch_layer("_scratch_v12.lua", [[
return {
    repos = { base = { type = "arch", mirror = "https://mirror.rackspace.com/archlinux", branches = {} } },
    system = {
        host_name = "test",
        locale = { default = "en_US.UTF-8" }
    },
    boot = { bootloader = { type = "systemd-boot", efi = true, timeout = 10 },
        kernel = {} },
    storage = { devices = { disk0 = { device = "/dev/nvme0n1", type = "gpt", efi = true,
        partitions = { { name = "boot", size = "512MB", type = "esp",
            filesystem = "vfat", mountpoint = "/boot" } } } } },
    generations = { storage = "store", root = "/kod" },
    desktop = {
        display_manager = "cosmic-greeter",
        desktops = {
            gnome = {
                enable = true,
                extra_packages = { { repo = "base", name = "gnome-tweaks" } }
            },
            cosmic = {
                enable = true
            }
        }
    },
    users = {},
    packages = {}
}
]])
-- Should now validate without error (no desktop field errors)
assert(not has("desktop: unknown key"), "desktop section should be a valid field")

-- Test: AUR packages in user environment
has = scratch_layer("_scratch_v13.lua", [[
return {
    repos = { base = { type = "arch", mirror = "https://mirror.rackspace.com/archlinux", branches = {} } },
    system = { host_name = "test", locale = { default = "en_US.UTF-8" } },
    boot = { bootloader = { type = "systemd-boot", efi = true, timeout = 10 },
        kernel = {} },
    storage = { devices = { disk0 = { device = "/dev/nvme0n1", type = "gpt", efi = true,
        partitions = { { name = "boot", size = "512MB", type = "esp",
            filesystem = "vfat", mountpoint = "/boot" } } } } },
    generations = { storage = "store", root = "/kod" },
    users = {
        testuser = {
            name = "Test User",
            group = "users",
            home_directory = "/home/testuser",
            shell = "/bin/bash",
            create_home = true,
            environment = {
                packages = {
                    { repo = "base", name = "git" },
                    { repo = "aur", name = "visual-studio-code-bin" },
                    { repo = "aur", name = "quickemu" }
                }
            }
        }
    },
    packages = {}
}
]])
-- Should validate without error (tagged_list already allows any repo string)
assert(not has("repo.*aur.*unknown"), "AUR packages in user environment should be valid")

print("PASS: schema validation (clean configs + AUR validation test)")
