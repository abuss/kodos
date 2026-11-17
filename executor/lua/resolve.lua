-- resolve.lua — shared config-layer machinery for the eszkoz dialect.
-- Used by check-packages.lua, generate.lua, and the merge test.
--
-- is_array/deep_merge are a local copy of proc-lua-conf demo.lua's merge
-- (semantics kept identical): array+array -> concatenate, dict+dict ->
-- recursive overlay with b winning, non-table -> b wins.
-- Identity-carrying sections (services, users) are DICTS keyed by name,
-- so layers merge them natively — no special merge rules.
local M = {}

local function is_array(t)
    if type(t) ~= "table" then return false end
    local count = 0
    for k in pairs(t) do
        if type(k) ~= "number" or k < 1 or k % 1 ~= 0 then return false end
        count = count + 1
    end
    for i = 1, count do
        if t[i] == nil then return false end
    end
    return true
end
M.is_array = is_array

local function deep_merge(a, b)
    if type(a) ~= "table" or type(b) ~= "table" then return b end
    if is_array(a) and is_array(b) then
        local res = {}
        for _, v in ipairs(a) do res[#res + 1] = v end
        for _, v in ipairs(b) do res[#res + 1] = v end
        return res
    end
    local res = {}
    for k, v in pairs(a) do res[k] = v end
    for k, v in pairs(b) do
        if type(v) == "table" and type(res[k]) == "table" then
            res[k] = deep_merge(res[k], v)
        else
            res[k] = v
        end
    end
    return res
end
M.deep_merge = deep_merge

-- load and merge config files in order (layer 1 <- layer 2 <- ...)
function M.load(paths)
    local conf = {}
    for _, p in ipairs(paths) do
        conf = deep_merge(conf, dofile(p))
    end
    return conf
end

-- distro inference: repos.base.type picks the backend (single source of truth)
local backend_registry = { apk = "backends.alpine", arch = "backends.arch",
                           deb = "backends.deb" }

function M.backend_for(conf)
    local rtype = conf.repos.base.type
    return require(backend_registry[rtype]
                   or error("no backend for repo type " .. tostring(rtype)))
end

-- collect package names tagged to a repo ("base" = canonical distro names,
-- "flatpak" = Flathub app ids), deduplicated, stable first-seen order
function M.names_in(t, repo)
    local names, seen = {}, {}
    local function walk(x)
        for _, v in pairs(x) do
            if type(v) == "table" then
                if v.repo == repo and v.name and not seen[v.name] then
                    seen[v.name] = true
                    names[#names + 1] = v.name
                elseif type(v) == "table" then
                    walk(v)
                end
            end
        end
    end
    walk(t)
    return names
end

function M.canonical_names(conf) return M.names_in(conf, "base") end

-- canonical names out of a tagged package list (service/user level lists)
function M.tagged_canons(list)
    local out = {}
    for _, t in ipairs(list or {}) do
        if t.repo == "base" and t.name then out[#out + 1] = t.name end
    end
    return out
end

-- flatpak apps split into install scopes: system (everything outside the
-- users section) vs per-user (the user's environment). ids sorted.
function M.flatpak_plan(conf)
    local outside = {}
    for k, v in pairs(conf) do
        if k ~= "users" then outside[k] = v end
    end
    local system = M.names_in(outside, "flatpak")
    table.sort(system)
    local users = {}
    for _, u in ipairs(M.user_names(conf)) do
        local ids = M.names_in((conf.users[u].environment or {}), "flatpak")
        if #ids > 0 then
            table.sort(ids)
            users[#users + 1] = { user = u, ids = ids }
        end
    end
    return { remote = conf.repos and conf.repos.flatpak,
             system = system, users = users }
end

-- Flathub app id shape: reverse-DNS components (Lua patterns cannot do
-- this in one expression -- repeated capture groups backtrack poorly)
function M.is_app_id(id)
    if id:match("^%.") or id:match("%.$") or id:find("%.%.") then
        return false
    end
    local parts = 0
    for part in id:gmatch("[^%.]+") do
        parts = parts + 1
        if not part:match("^[%w][%w_%-]*$") then return false end
    end
    return parts >= 2
end

-- the FULL distro package install set: backend base + kernel variant +
-- config canonical (mapped) + service/home packages + bootloader + locale
-- + flatpak tool. Sorted, deduped. Also returns dropped canonical names.
function M.install_set(conf, backend)
    local _, kpkgs = M.kernel(conf, backend)
    local bl = conf.boot.bootloader
    local blpkgs = backend.bootloader_packages and backend.bootloader_packages[bl.type]
    if not blpkgs then
        error("bootloader '" .. bl.type .. "' not offered by backend '"
              .. backend.name .. "'")
    end
    local cfgnames, dropped = M.resolve_names(M.canonical_names(conf), backend)
    local all, seen = {}, {}
    local function addp(list)
        for _, n in ipairs(list or {}) do
            if not seen[n] then seen[n] = true; all[#all + 1] = n end
        end
    end
    addp(backend.base_packages)
    addp(kpkgs)
    addp(blpkgs)
    addp(cfgnames)
    -- local repo packages: tagged {repo=<reponame>} where reponame is a key in conf.repositories
    for rname in pairs(conf.repositories or {}) do
        addp(M.names_in(conf, rname))
    end
    for _, s in ipairs(M.service_plan(conf, backend)) do addp(s.packages) end
    for _, s in ipairs(M.home_service_plan(conf, backend)) do addp(s.packages) end
    if conf.system.locale then addp(backend.locale_packages) end
    if conf.repos.flatpak then addp(backend.flatpak_packages) end
    table.sort(all)
    return all, dropped
end

-- usernames, sorted (users are a dict keyed by login)
function M.user_names(conf)
    local out = {}
    for n in pairs(conf.users or {}) do out[#out + 1] = n end
    table.sort(out)
    return out
end

-- canonical -> distro name (string=map, false=drop with warning, nil=identity)
function M.resolve_names(names, backend)
    local resolved, dropped = {}, {}
    for _, n in ipairs(names) do
        local m = backend.map and backend.map[n]
        if m == false then
            dropped[#dropped + 1] = n
        else
            resolved[#resolved + 1] = m or n
        end
    end
    return resolved, dropped
end

-- kernel: boot.kernel.variant (absent = "default"); a variant the backend
-- does not offer is a hard error (never silently swap the user's kernel).
function M.kernel(conf, backend)
    local v = (conf.boot and conf.boot.kernel and conf.boot.kernel.variant)
              or "default"
    local variants = backend.kernel_packages or {}
    if not variants[v] then
        local have = {}
        for k in pairs(variants) do have[#have + 1] = k end
        table.sort(have)
        error("kernel variant '" .. v .. "' not offered by backend '"
              .. backend.name .. "'; variants: " .. table.concat(have, ", "))
    end
    return v, variants[v]
end

-- service plan: enabled services resolved to units/runlevels/distro packages.
-- Backend baseline packages are already distro names; config packages are
-- canonical (additive, mapped through backend.map).
-- settings = the remaining keys of the config entry (connections is NOT a
-- setting: it gets its own configure_network action, see generate.lua)
local SKIP = { enable = true, packages = true, service_name = true,
               connections = true }
function M.service_plan(conf, backend)
    local plan = {}
    for name, srv in pairs(conf.services or {}) do
        if srv.enable then
            local be = (backend.services or {})[name]
            local unit = be and be.service_name
            if unit == nil then unit = srv.service_name end
            if unit == nil then unit = name end -- escape hatch: key = unit
            local pkgs = {}
            for _, p in ipairs(be and be.packages or {}) do
                pkgs[#pkgs + 1] = p
            end
            for _, p in ipairs(M.resolve_names(M.tagged_canons(srv.packages), backend)) do
                pkgs[#pkgs + 1] = p
            end
            local settings = {}
            local has_settings = false
            for k, v in pairs(srv) do
                if not SKIP[k] then settings[k] = v; has_settings = true end
            end
            -- fail CLOSED at generate time: settings are only appliable via a
            -- backend-defined settings file, and only ssh has one today. A
            -- stray key here used to reach the executor as an anonymous
            -- "configure_service: no settings_file" at exec time.
            if has_settings and not (be and be.settings_file) then
                local ks = {}
                for k in pairs(settings) do ks[#ks + 1] = k end
                table.sort(ks)
                error("service " .. name .. ": settings (" ..
                      table.concat(ks, ", ") .. ") but the backend defines no settings file for it")
            end
            plan[#plan + 1] = {
                name = name,
                unit = unit ~= false and unit or nil, -- false = packages only
                runlevel = be and be.runlevel or "default",
                extra = be and be.extra or nil, -- backend-required helper units
                packages = pkgs,
                settings = has_settings and settings or nil,
                settings_file = be and be.settings_file or nil,
            }
        end
    end
    table.sort(plan, function(x, y) return x.name < y.name end)
    return plan
end

-- per-user (home) services: backend picks mechanism + instance naming
function M.home_service_plan(conf, backend)
    local plan = {}
    for uname, u in pairs(conf.users or {}) do
        for name, srv in pairs((u.environment or {}).services or {}) do
            if srv.enable then
                local be = (backend.home_services or {})[name]
                local pkgs = {}
                for _, p in ipairs(be and be.packages or {}) do pkgs[#pkgs + 1] = p end
                for _, p in ipairs(M.resolve_names(M.tagged_canons(srv.packages), backend)) do
                    pkgs[#pkgs + 1] = p
                end
                plan[#plan + 1] = {
                    user = uname, name = name,
                    mechanism = be and be.mechanism or nil,
                    packages = pkgs,
                    settings = srv.settings,
                }
            end
        end
    end
    table.sort(plan, function(x, y)
        return x.user < y.user or (x.user == y.user and x.name < y.name)
    end)
    return plan
end

return M
