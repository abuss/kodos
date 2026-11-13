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
    local h = assert(io.popen("lua lockfile.lua " .. table.concat({ ... }, " ")))
    local out = h:read("a")
    local ok = h:close()
    return out, ok
end

-- ====== positive: layered eszkoz, generation 7 ======
local out, ok = run("7 " .. IDX[1] .. " " .. IDX[2]
                    .. " -- system.lua layers/machine.lua layers/user.lua")
assert(ok, "lockfile failed:\n" .. out)

assert(out:find('"generation": 7', 1, true))
assert(out:find('"kind": "kodos-generation"', 1, true))
assert(out:find('"distro": "alpine"', 1, true))
assert(out:find('"kernel": "linux-lts-6', 1, true), "kernel not pinned")

-- every locked package carries BOTH version and checksum, and the counts
-- agree with the reported locked_count
local n = tonumber(out:match('"locked_count": (%d+)'))
assert(n and n > 80, "locked_count suspicious: " .. tostring(n))
local vers, checs = 0, 0
for _ in out:gmatch('"version": "') do vers = vers + 1 end
for _ in out:gmatch('"checksum": "') do checs = checs + 1 end
assert(vers == n and checs == n,
       ("counts: %d names, %d versions, %d checksums"):format(n, vers, checs))

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
                  .. " -- system.lua layers/machine.lua layers/user.lua")
assert(out == again, "lock output is not deterministic")

-- ====== negative: a name the index does not have must abort ======
local f = assert(io.open("layers/_scratch_lock.lua", "w"))
f:write('return { packages = { { repo = "base", name = "pkg-that-does-not-exist-xyz" } } }')
f:close()
local h = assert(io.popen("lua lockfile.lua 0 " .. IDX[1] .. " " .. IDX[2]
                          .. " -- system.lua layers/_scratch_lock.lua 2>&1"))
local err = h:read("a")
local bad = h:close()
os.remove("layers/_scratch_lock.lua")
assert(not bad, "missing package must abort the lock")
assert(err:find("pkg-that-does-not-exist-xyz not found in indexes", 1, true))

print("PASS: version lock (" .. n .. " pkgs pinned, deterministic, strict)")
