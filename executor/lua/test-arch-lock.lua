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

-- versionless PROVIDES entries (the REAL arch shape: bash provides bare
-- "sh") are candidates too, and real sync DBs carry %SHA256SUM%
local bare = alpm.parse([[
%FILENAME%
core/os/x86_64/dash-0.5.12-1.pkg.tar.zst
%NAME%
dash
%VERSION%
0.5.12-1
%PROVIDES%
sh
%DEPENDS%

%SHA256SUM%
deadbeef
]], "core")
assert(bare.dash.checksum == "deadbeef", "%SHA256SUM% accepted as checksum")
local shell_user = alpm.parse([[
%FILENAME%
core/os/x86_64/shell-user-1-1.pkg.tar.zst
%NAME%
shell-user
%VERSION%
1-1
%PROVIDES%

%DEPENDS%
sh
%CHKSUM%
cafe
]], "core")
for k, v in pairs(shell_user) do bare[k] = v end
locked = alpm.closure({ "shell-user" }, bare)
assert(locked.dash, "bare 'sh' provides must satisfy dep sh")

-- a versioned dep the only provider cannot satisfy fails closed
local ok = pcall(alpm.closure, { "strict-boot" }, idx)
assert(not ok, "kernel>=7 against linux=6.12 must fail")

-- real arch dep specs cite pkgver WITHOUT pkgrel: util-linux depends on
-- "util-linux-libs=2.42.4" while the package is 2.42.4-1 (measured Nov 2026)
local relax = alpm.parse([[
%FILENAME%
core/os/x86_64/util-linux-2.42.4-1.pkg.tar.zst
%NAME%
util-linux
%VERSION%
2.42.4-1
%PROVIDES%

%DEPENDS%
util-linux-libs=2.42.4
%CHKSUM%
c1

%FILENAME%
core/os/x86_64/util-linux-libs-2.42.4-1.pkg.tar.zst
%NAME%
util-linux-libs
%VERSION%
2.42.4-1
%PROVIDES%

%DEPENDS%

%CHKSUM%
c2
]], "core")
locked = alpm.closure({ "util-linux" }, relax)
assert(locked["util-linux-libs"], "= dep on pkgver must match pkg 2.42.4-1")

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
