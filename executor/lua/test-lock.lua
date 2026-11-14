-- test-lock.lua — version lock against REAL index snapshots.
-- Needs extracted apkindex text (regen after drift):
--   for r in main community; do curl -sf \
--     "https://dl-cdn.alpinelinux.org/alpine/v3.24/$r/x86_64/APKINDEX.tar.gz" \
--     | tar -xzO APKINDEX > /tmp/opencode/$r-324.apkindex; done
local IDX = { "/tmp/opencode/main-324.apkindex", "/tmp/opencode/community-324.apkindex" }
for _, p in ipairs(IDX) do
    local f = io.open(p)
    if not f then
        print("SKIP: missing index " .. p .. " (see header for the curl|tar recipe)")
        os.exit(0)
    end
    f:close()
end

local function run(...)
    if os.getenv("KODOS_INPROCESS") == "1" then
        local args = {}
        for _, s in ipairs({ ... }) do
            for w in s:gmatch("%S+") do args[#args + 1] = w end
        end
        return kodos_run("lockfile.lua", unpack(args)), true -- Lua 5.1: table.unpack is just unpack
    end
    local cmd = (os.getenv("KODOS_LUA_CMD") or "lua") .. " lockfile.lua " .. table.concat({ ... }, " ")
    local h = assert(io.popen(cmd))
    local out = h:read("*a")
    local ok = h:close()
    return out, ok
end

-- ====== positive: layered eszkoz, generation 7 ======
local out, ok = run("7 " .. IDX[1] .. " " .. IDX[2]
                    .. " -- system.lua ../../example/eszkoz/layers/machine.lua ../../example/eszkoz/layers/user.lua")
assert(ok, "lockfile failed:\n" .. out)

assert(out:find('"generation": 7', 1, true))
assert(out:find('"kind": "kodos-generation"', 1, true))
assert(out:find('"distro": "alpine"', 1, true))
assert(out:find('"kernel": "linux-lts-6', 1, true), "kernel not pinned")

-- every locked package carries BOTH version and checksum, and the counts
-- agree with the reported locked_count. Closure lock: locked_count covers
-- the full apk closure (requests + transitive deps), far above the requests.
local n = tonumber(out:match('"locked_count": (%d+)'))
local req = tonumber(out:match('"requested_count": (%d+)'))
assert(n and n > 400, "locked_count suspicious: " .. tostring(n))
assert(req and req < n / 3, ("closure lock looks off: %d locked vs %d requested"):format(n, req))
local vers, checs = 0, 0
for _ in out:gmatch('"version": "') do vers = vers + 1 end
for _ in out:gmatch('"checksum": "') do checs = checs + 1 end
assert(vers == n and checs == n,
       ("counts: %d names, %d versions, %d checksums"):format(n, vers, checs))
-- transitive deps pinned too (neither name appears in any config layer)
assert(out:find('"libcrypto3": {', 1, true), "closure lock: dep libcrypto3 not pinned")
assert(out:find('"dbus-libs": {', 1, true), "closure lock: dep dbus-libs not pinned")

-- distro-name locking (canonical mapping applied BEFORE locking) and
-- layering honored: clang20 (not clang19) locked, cups gone, tz/locale
-- backend packages pinned
assert(out:find('"clang20": {', 1, true))
assert(not out:find('"clang19"', 1, true))
assert(not out:find('"cups"', 1, true), "disabled service must not lock packages")
assert(out:find('"musl-locales": {', 1, true))
assert(out:find('"linux-firmware": {', 1, true))

-- ====== determinism: same index -> byte-identical lock ======
local again = run("7 " .. IDX[1] .. " " .. IDX[2]
                  .. " -- system.lua ../../example/eszkoz/layers/machine.lua ../../example/eszkoz/layers/user.lua")
assert(out == again, "lock output is not deterministic")

-- ====== negative: a name the index does not have must abort ======
local f = assert(io.open("_scratch_lock.lua", "w"))
f:write('return { packages = { { repo = "base", name = "pkg-that-does-not-exist-xyz" } } }')
f:close()
local err, bad
if os.getenv("KODOS_INPROCESS") == "1" then
    local ok, e = pcall(kodos_run, "lockfile.lua",
        "0", IDX[1], IDX[2], "--", "system.lua", "_scratch_lock.lua")
    err = ok and "" or tostring(e)
    bad = ok  -- ok=false means script aborted (expected); bad=false => assert(not bad) passes
else
    local h = assert(io.popen((os.getenv("KODOS_LUA_CMD") or "lua") .. " lockfile.lua 0 " .. IDX[1] .. " " .. IDX[2]
                              .. " -- system.lua _scratch_lock.lua 2>&1"))
    err = h:read("*a")
    bad = h:close()
end
os.remove("_scratch_lock.lua")
assert(not bad, "missing package must abort the lock")
assert(err:find("pkg-that-does-not-exist-xyz not found in indexes", 1, true))

print("PASS: version lock (" .. n .. " pkgs pinned, deterministic, strict)")
