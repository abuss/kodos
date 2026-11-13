-- lockfile.lua — version lock (NEXT.md step 4): resolve the install set
-- against real distro indexes and emit generation-N.json: every package
-- pinned to the exact version+checksum found in the index. The executor
-- installs EXACTLY these; rollback = activate an older generation file.
--
-- Usage: lua lockfile.lua <generation-number> <APKINDEX...> -- <config.lua> [layer.lua ...]
-- Index input: extracted apk index text (`tar -xzOf main.apkindex.tar.gz APKINDEX`),
-- or .tar.gz directly (auto-extracted via tar). Output: JSON on stdout.
--
-- Deviation from the GENERATION_STORAGE_SUMMARY sketch: packages map to
-- {version, checksum} records (the IR {name,version,checksum} hook), and
-- timestamp/config_hash are omitted HERE so this output is byte-deterministic
-- from a given index snapshot; the executor stamps those at creation time.
local resolve = require("resolve")
local validate = require("validate")
local json = require("json")

-- parse one apk index (stanza-per-package; P=name V=version C=checksum,
-- single-letter field keys, blank-line separated)
local function parse_apk_index(text, into)
    local cur = {}
    local function commit()
        if cur.P then
            into[cur.P] = { version = cur.V, checksum = cur.C }
        end
        cur = {}
    end
    for line in (text .. "\n\n"):gmatch("([^\n]*)\n") do
        if line == "" then
            commit()
        else
            local k, v = line:match("^([A-Z]):(.*)$")
            if k then cur[k] = v end
        end
    end
    commit()
end

local function read_index(path)
    local text
    if path:match("%.tar%.gz$") then
        local h = assert(io.popen("tar -xzOf '" .. path .. "' APKINDEX"),
                         "cannot extract APKINDEX from " .. path)
        text = h:read("a")
        h:close()
    else
        text = assert(io.open(path)):read("a")
    end
    local pkgs = {}
    parse_apk_index(text, pkgs)
    return pkgs
end

-- ====== CLI ======
local sep
for i = 1, #arg do
    if arg[i] == "--" then sep = i break end
end
if not sep or sep < 3 then
    print("usage: lua lockfile.lua <generation> <APKINDEX...> -- <config.lua> [layer.lua ...]")
    os.exit(1)
end
local generation = assert(tonumber(arg[1]), "generation number must be an integer")
local index = {}
for i = 2, sep - 1 do
    for name, rec in pairs(read_index(arg[i])) do
        -- first index wins (apk resolution order: repos later in the file
        -- are lower priority; pass main before community)
        if not index[name] then index[name] = rec end
    end
end
local cfiles = {}
for i = sep + 1, #arg do cfiles[#cfiles + 1] = arg[i] end

local conf = resolve.load(cfiles)
local serrs = validate.validate(conf)
if #serrs > 0 then
    for _, e in ipairs(serrs) do io.stderr:write("SCHEMA ERROR: " .. e .. "\n") end
    os.exit(1)
end
local backend = resolve.backend_for(conf)

-- resolve + lock every distro package name
local names, dropped = resolve.install_set(conf, backend)
local missing, locked = 0, {}
for _, n in ipairs(names) do
    local rec = index[n]
    if not rec or not rec.version then
        io.stderr:write("LOCK ERROR: " .. n .. " not found in indexes\n")
        missing = missing + 1
    else
        locked[n] = { version = rec.version, checksum = rec.checksum }
    end
end

-- flatpak apps cannot be version-locked offline (Flathub is the authority);
-- recorded as floating ids until an online lock exists
local fp = resolve.flatpak_plan(conf)

if missing > 0 then
    io.stderr:write(string.format("%d/%d names could not be locked\n",
                                  missing, #names))
    os.exit(1)
end

local kernel_variant, kpkgs = resolve.kernel(conf, backend)
print(json.encode({
    schema_version = 1,
    kind = "kodos-generation",
    generation = generation,
    distro = backend.name,
    kernel_variant = kernel_variant,
    kernel = kpkgs[1] .. "-" .. locked[kpkgs[1]].version,
    packages = locked,
    flatpak = fp,
    locked_count = #names,
}))
if #dropped > 0 then
    io.stderr:write("dropped by backend: " .. table.concat(dropped, ", ") .. "\n")
end
