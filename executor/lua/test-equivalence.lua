-- test-equivalence.lua — prove the monolithic and modular forms describe
-- the same system config. Run: lua test-equivalence.lua (from this dir)

local function load_modular()
    package.loaded["kodos"] = nil
    package.loaded["home"] = nil
    package.loaded["modules.users"] = nil
    package.loaded["modules.services"] = nil
    package.loaded["modules.filesystems"] = nil
    package.loaded["packages.all-packages"] = nil
    return dofile("system.lua")
end

local function load_monolithic()
    return dofile("../../example/eszkoz/configuration.lua")
end

local function same(a, b, path)
    if type(a) ~= type(b) then
        return nil, path .. ": type " .. type(a) .. " vs " .. type(b)
    end
    if type(a) ~= "table" then
        if a ~= b then return nil, path .. ": " .. tostring(a) .. " vs " .. tostring(b) end
        return true
    end
    for k, v in pairs(a) do
        if b[k] == nil then return nil, path .. "." .. tostring(k) .. ": missing in " end
        local ok, why = same(v, b[k], path .. "." .. tostring(k))
        if not ok then return nil, why end
    end
    for k in pairs(b) do
        if a[k] == nil then return nil, path .. "." .. tostring(k) .. ": missing in configuration.lua" end
    end
    return true
end

local mono, modu = load_monolithic(), load_modular()
local ok, why = same(mono, modu, "$")
if ok then
    local n = 0
    for _ in pairs(mono) do n = n + 1 end
    print("PASS: monolithic and modular forms are equivalent (" .. n .. " top-level sections)")
else
    print("FAIL: " .. why)
    os.exit(1)
end
