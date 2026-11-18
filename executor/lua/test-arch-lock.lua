-- test-arch-lock.lua — ALPM index parse + closure semantics (arch lockfile).
-- Pure data: runs under lua and gopher-lua (no popen). Fixture:
-- test-fixture/core.desc (plain ALPM desc text; repo derived from file name).
local alpm = require("alpm")

-- locate the fixture under both plausible cwds (executor/lua, executor)
local fixture, text
for _, p in ipairs({ "test-fixture/core.desc", "lua/test-fixture/core.desc",
                     "executor/lua/test-fixture/core.desc" }) do
    local h = io.open(p)
    if h then text = h:read("*a") h:close() fixture = p break end
end
assert(text, "test-fixture/core.desc not found from cwd")

-- is_alpm: true on ALPM desc text, false on apk index text
assert(alpm.is_alpm(text) == true, "fixture must be detected as ALPM")
assert(alpm.is_alpm("P:glibc\nV:2.40-1\nC:abc\n") == false, "apk text is not ALPM")

-- parse
local idx = alpm.parse(text, "core")
assert(idx.glibc.version == "2.40-1", "glibc version")
assert(idx.glibc.checksum == string.rep("0", 63) .. "1", "glibc chksum")
assert(idx.glibc.repo == "core", "repo passed through")
assert(#idx.glibc.depends == 2, "glibc depends count")
assert(#idx.linux.provides == 2, "linux provides count")
assert(idx.filesystem.depends[1] == nil, "empty DEPENDS stays empty")

-- vercmp (alpm semantics)
assert(alpm.vercmp("1.2-3", "1.10-1") == -1, "1.2-3 < 1.10-1")
assert(alpm.vercmp("2:1.0", "1:9.9") == 1, "epoch dominates")
assert(alpm.vercmp("1.0-1", "1.0-1") == 0, "equal")

-- closure: exact pins, repos and checksums carried
local locked = alpm.closure({ "glibc" }, idx)
local count = 0
for _ in pairs(locked) do count = count + 1 end
assert(count == 4, "glibc closure = glibc+linux+filesystem+core-utils, got " .. count)
assert(locked.linux.version == "6.12-1", "linux pinned by depends linux>=6.6")
assert(locked["core-utils"].checksum == string.rep("0", 63) .. "4", "transitive pin")

-- a bare-name dep is satisfied through PROVIDES (kernel -> linux)
locked = alpm.closure({ "boot-helper" }, idx)
assert(locked.linux and locked.linux.version == "6.12-1", "kernel provided by linux")

-- so: deps match providers only
locked = alpm.closure({ "ssl-user" }, idx)
assert(locked.linux, "so:libcrypto.so.3 satisfied by linux's provides")

-- a versioned dep the only provider cannot satisfy fails closed
local ok = pcall(alpm.closure, { "strict-boot" }, idx)
assert(not ok, "kernel>=7 against linux=6.12 must fail")

-- dependency cycles terminate (a <-> b)
local cyc = alpm.parse([[
%FILENAME%
core/os/x86_64/pa-1-1.pkg.tar.zst
%NAME%
pa
%VERSION%
1-1
%PROVIDES%

%DEPENDS%
pb
%CHKSUM%
c1

%FILENAME%
core/os/x86_64/pb-1-1.pkg.tar.zst
%NAME%
pb
%VERSION%
1-1
%PROVIDES%

%DEPENDS%
pa
%CHKSUM%
c2
]], "core")
locked = alpm.closure({ "pa" }, cyc)
assert(locked.pa and locked.pb, "cycle resolves")

print("test-arch-lock: ok")
