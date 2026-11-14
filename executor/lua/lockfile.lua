-- lockfile.lua — version lock (NEXT.md step 4): resolve the install set
-- against real distro indexes and emit generation-N.json: EVERY package
-- (config requests AND the full dependency closure, locked via the repo's
-- static apk-tools against the same snapshots) pinned to the exact
-- version+checksum found in the index. The executor installs EXACTLY
-- these; rollback = activate an older generation file.
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
        text = h:read("*a")
        h:close()
    else
        text = assert(io.open(path)):read("*a")
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
local index, allrec, ipaths = {}, {}, {}
for i = 2, sep - 1 do
    ipaths[#ipaths + 1] = arg[i]
    for name, rec in pairs(read_index(arg[i])) do
        allrec[name .. "|" .. rec.version] = rec.checksum
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

-- ====== closure lock ======
-- config names are REQUESTS, not a closure: busybox/musl/so: providers
-- arrive only via depends. Lock the FULL closure with the same solver the
-- executor uses (the repo's static apk-tools), simulating the pinned
-- request set against exactly these index snapshots (packaged as
-- file:// repos). Every package the machine needs ends up pinned; the
-- executor then re-resolves live and hard-errors on any version drift.
local apkbin = os.getenv("KODOS_APK") or "../../vm/apk-tools-static"
if not io.open(apkbin) then
    io.stderr:write("LOCK ERROR: closure lock needs static apk-tools "
                    .. "(KODOS_APK=/path, looked in " .. apkbin .. ")\n")
    os.exit(1)
end
local function sh(cmd)
    local ok = os.execute(cmd)
    if ok ~= true and ok ~= 0 then error("command failed: " .. cmd, 0) end
end

local tmp = (os.getenv("TMPDIR") or "/tmp") .. "/kodos-lock-"
            .. tostring(os.time()) .. "-" .. tostring(math.random(100000))
sh(("mkdir -p '%s/db/lib/apk/db' '%s/db/etc/apk'"):format(tmp, tmp))
sh(("touch '%s/db/lib/apk/db/installed' '%s/db/etc/apk/world'"):format(tmp, tmp))
local repoArgs = {}
for i, path in ipairs(ipaths) do
    -- apk fetches <repo>/<arch>/APKINDEX.tar.gz and wants the member named
    -- APKINDEX; arch is fixed x86_64 because the vendored static apk-tools is
    local rdir = tmp .. "/r" .. i
    sh(("mkdir -p '%s/x86_64' '%s/stage'"):format(rdir, tmp))
    if path:match("%.tar%.gz$") then
        sh(("cp '%s' '%s/x86_64/APKINDEX.tar.gz'"):format(path, rdir))
    else
        sh(("cp '%s' '%s/stage/APKINDEX' && tar -czf '%s/x86_64/APKINDEX.tar.gz' -C '%s/stage' APKINDEX"):
            format(path, tmp, rdir, tmp))
    end
    repoArgs[#repoArgs + 1] = "-X file://" .. rdir
end
local pins = {}
for name, rec in pairs(locked) do pins[#pins + 1] = name .. "=" .. rec.version end
table.sort(pins) -- stable argument order -> deterministic simulate output
local h = assert(io.popen(("'%s' --root '%s/db' --no-cache --allow-untrusted %s "
                          .. "add --simulate %s 2>/dev/null"):
    format(apkbin, tmp, table.concat(repoArgs, " "), table.concat(pins, " "))))
local simout = h:read("*a")
h:close()
sh(("rm -rf '%s'"):format(tmp))

local closure = 0
for line in simout:gmatch("[^\n]+") do
    local i = line:find(") Installing ", 1, true)
    if i then
        local name, ver = line:sub(i + 13):match("^(%S+)%s+%((.-)%)")
        if locked[name] then
            closure = closure + 1 -- already pinned as a request
        else
            local chc = allrec[name .. "|" .. ver]
            if not chc then
                io.stderr:write(("LOCK ERROR: dep %s-%s resolved by apk is not "
                                 .. "in the index snapshots\n"):format(name, ver))
                os.exit(1)
            end
            locked[name] = { version = ver, checksum = chc }
            closure = closure + 1
        end
    end
end
if closure == 0 then
    io.stderr:write("LOCK ERROR: apk closure resolve produced nothing:\n" .. simout)
    os.exit(1)
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
local total = 0
for _ in pairs(locked) do total = total + 1 end
print(json.encode({
    schema_version = 1,
    kind = "kodos-generation",
    generation = generation,
    distro = backend.name,
    kernel_variant = kernel_variant,
    kernel = kpkgs[1] .. "-" .. locked[kpkgs[1]].version,
    packages = locked,
    flatpak = fp,
    locked_count = total,      -- full closure (requests + deps)
    requested_count = #names,  -- config-declared names before apk closure
}))
if #dropped > 0 then
    io.stderr:write("dropped by backend: " .. table.concat(dropped, ", ") .. "\n")
end
