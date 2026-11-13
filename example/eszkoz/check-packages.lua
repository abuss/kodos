-- check-packages.lua — distro-neutral config + backend, end to end.
-- Resolves every canonical package name through the backend picked by
-- repos.base.type, then verifies all resulting names against real distro
-- package lists. Backend-supplied packages are verified too.
-- Config entry file is a parameter (like the generator's -c): the default
-- is the modular system.lua; pass configuration.lua to check the
-- monolithic form directly. (test-equivalence.lua covers "both forms are
-- the same table"; this covers "that table names real things".)
-- One or more config files after the indexes are MERGED left-to-right,
-- so layers can be validated too:
--   lua check-packages.lua main.pkgs community.pkgs \
--       system.lua layers/machine.lua layers/user.lua
-- Usage: lua check-packages.lua <main.pkgs> <community.pkgs> [config layer...]

local resolve = require("resolve")

local valid = {}
for _, f in ipairs({ arg[1] or "main.pkgs", arg[2] or "community.pkgs" }) do
    local fh = assert(io.open(f))
    for line in fh:lines() do valid[line] = true end
end

local cfiles = {}
for i = 3, #arg do cfiles[#cfiles + 1] = arg[i] end
if #cfiles == 0 then cfiles = { "system.lua" } end
local conf = resolve.load(cfiles)

-- schema first: generate would proceed on this config; validate it here too
local validate = require("validate")
local serrs = validate.validate(conf)
if #serrs > 0 then
    for _, e in ipairs(serrs) do print("SCHEMA ERROR: " .. e) end
    os.exit(1)
end

-- distro inference: repos.base.type must select a backend
local backend = resolve.backend_for(conf)
assert(backend.repo_types[conf.repos.base.type],
       "backend mismatch for repo type " .. conf.repos.base.type)

local names = resolve.canonical_names(conf)
-- canonical -> distro name (string=map, false=drop with warning, nil=identity)
local resolved, dropped = resolve.resolve_names(names, backend)

local missing, checked_count = 0, 0

-- backend-supplied packages get verified too (the registry must be honest)
for _, list in pairs({ backend.base_packages, backend.flatpak_packages }) do
    for _, n in ipairs(list or {}) do resolved[#resolved + 1] = n end
end

-- kernel: config picks a NEUTRAL variant (boot.kernel.variant; absent =
-- "default"). A variant the backend does not offer is an error; every
-- variant the backend DOES offer is validated against the distro index.
local kvariant = (conf.boot.kernel and conf.boot.kernel.variant) or "default"
local variants = backend.kernel_packages or {}
if not variants[kvariant] then
    local have = {}
    for k in pairs(variants) do have[#have + 1] = k end
    table.sort(have)
    print("KERNEL ERROR: variant '" .. kvariant .. "' not offered by backend '"
          .. backend.name .. "'; variants: " .. table.concat(have, ", "))
    missing = missing + 1
end
for _, kl in pairs(variants) do
    for _, n in ipairs(kl) do resolved[#resolved + 1] = n end
end

for _, reg in pairs({ backend.services, backend.home_services }) do
    for _, s in pairs(reg) do
        for _, n in ipairs(s.packages or {}) do resolved[#resolved + 1] = n end
    end
end
for _, bl in pairs(backend.bootloader_packages or {}) do
    for _, n in ipairs(bl) do resolved[#resolved + 1] = n end
end

-- locale: only checked if the backend declares a fixed set (musl); backends
-- that can compile any locale (glibc locale-gen) declare no list -> skip.
local want = conf.system.locale and conf.system.locale.default
if want and backend.locales_supported and #backend.locales_supported > 0 then
    local supported = {}
    for _, l in ipairs(backend.locales_supported) do
        supported[l:match("^[^%.]+")] = true
    end
    if not supported[want:match("^[^%.]+")] then
        print("LOCALE ERROR: " .. want .. " not available; pick from: "
              .. table.concat(backend.locales_supported, ", "))
        missing = missing + 1
    end
end
for _, n in ipairs(backend.locale_packages or {}) do
    resolved[#resolved + 1] = n
end

-- services (enable rule lives in validate.lua): an enabled service with no
-- backend entry is allowed only via the escape hatch (config-provided packages)
for name, srv in pairs(conf.services or {}) do
    if srv.enable and not backend.services[name] then
        if type(srv.packages) ~= "table" or #srv.packages == 0 then
            print("SERVICE ERROR: '" .. name .. "' has no backend entry; the"
                  .. " escape hatch requires packages = base(...)")
            missing = missing + 1
        end
    end
end
-- flatpak: app ids are Flathub names, NOT distro-index entries: check the
-- reverse-DNS shape and that repos.flatpak is declared. Online existence
-- against Flathub itself is future CI.
local fp = resolve.flatpak_plan(conf)
local fp_count = #fp.system
for _, scope in ipairs(fp.users) do fp_count = fp_count + #scope.ids end
if fp.remote and fp.remote.type ~= "flatpak" then
    print("FLATPAK ERROR: repos.flatpak.type must be 'flatpak', got '"
          .. tostring(fp.remote.type) .. "'")
    missing = missing + 1
end
if (#fp.system > 0 or #fp.users > 0) and not fp.remote then
    print("FLATPAK ERROR: flatpak packages declared but repos.flatpak is missing")
    missing = missing + 1
end
for _, id in ipairs(fp.system) do
    if not resolve.is_app_id(id) then
        print("FLATPAK ERROR: '" .. id .. "' is not a reverse-DNS app id")
        missing = missing + 1
    end
end
for _, scope in ipairs(fp.users) do
    for _, id in ipairs(scope.ids) do
        if not resolve.is_app_id(id) then
            print("FLATPAK ERROR: '" .. id .. "' is not a reverse-DNS app id")
            missing = missing + 1
        end
    end
end

local checked = {}
for _, n in ipairs(resolved) do
    if not checked[n] then
        checked[n] = true
        checked_count = checked_count + 1
        if not valid[n] then print("MISSING: " .. n) missing = missing + 1 end
    end
end
if #dropped > 0 then
    print("dropped by backend: " .. table.concat(dropped, ", "))
end
if fp_count > 0 then
    print("flatpak: " .. fp_count .. " app ids (Flathub space, not distro-index checked)")
end
print(string.format("%d canonical names -> %d distro names checked, %d missing",
                    #names, checked_count, missing))
os.exit(missing == 0 and 0 or 1)
