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
must_pass({ "configuration.lua" }, "monolithic")
must_pass({ "system.lua", "layers/machine.lua", "layers/user.lua" }, "layered")

-- helper: validate base + scratch layer, assert error substrings appear
local function scratch_layer(name, body)
    local f = assert(io.open("layers/" .. name, "w"))
    f:write(body)
    f:close()
    local errs = validate.validate(resolve.load({ "system.lua", "layers/" .. name }))
    os.remove("layers/" .. name)
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

-- 5. valid: layer service overlay without enable (inherited from base) passes
has = scratch_layer("_scratch_v4.lua", [[
return { services = { audio = { packages = { { repo = "base", name = "htop" } } } } }
]])
local ok, why = has("NO-SUBSTR")
assert(ok == false, "should have no errors, got: " .. tostring(why))

print("PASS: schema validation (clean configs + 12 seeded errors caught)")
