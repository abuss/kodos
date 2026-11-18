-- test-generate.lua — pin the IR contract: run generate.lua (base and
-- layered) and assert shape, order, and layering effects. Run: lua test-generate.lua
local function run(...)
    if os.getenv("KODOS_INPROCESS") == "1" then
        -- Each vararg may be a space-joined string (matches host-popen style);
        -- split all args on spaces to get individual script arguments.
        local args = {}
        for _, s in ipairs({ ... }) do
            for w in s:gmatch("%S+") do args[#args + 1] = w end
        end
        return kodos_run("generate.lua", unpack(args)) -- Lua 5.1: table.unpack is just unpack
    end
    local cmd = (os.getenv("KODOS_LUA_CMD") or "lua") .. " generate.lua "
                .. table.concat({ ... }, " ") .. " 2>/dev/null"
    local h = assert(io.popen(cmd))
    local out = h:read("*a")
    h:close()
    assert(#out > 100, "generator produced no output")
    return out
end

local function pos(out, s)
    local p = out:find(s, 1, true)
    assert(p, "missing from IR: " .. s)
    return p
end

-- ====== base only: everything the config states directly ======
local base = run("system.lua")
assert(pos(base, '"unit": "cupsd"') > 0,
       "printing enabled in base config")
assert(pos(base, '"timeout": 10') > 0, "base bootloader timeout")
assert(pos(base, '"shell": "/usr/bin/zsh"') > 0, "base abuss shell")

-- /tmp is shared session state, not generation content: mount it as tmpfs
-- before activation so activateGeneration's mountpoint skip leaves the live
-- /tmp alone (no symlink into /kod/generation/current), and fstab remounts
-- it on every boot (fresh, 1777).
assert(base:find('tmpfs\\t/tmp\\ttmpfs\\t', 1, true),
       "fstab must mount shared /tmp as tmpfs")
assert(pos(base, '"partition": "tmpfs"') < pos(base, '"op": "create_generation"'),
       "tmpfs /tmp must be mounted before generation activation")

-- ====== layered: machine + user overlays change exactly what they say ======
local out = run("system.lua ../../example/eszkoz/layers/machine.lua ../../example/eszkoz/layers/user.lua")

-- backend resolution happened (distro names, not canonical)
assert(pos(out, '"clang20"') and not out:find("clang19", 1, true))
assert(pos(out, '"linux-lts"') and pos(out, '"musl-locales"'))
assert(pos(out, '"mirror": "https://dl-cdn.alpinelinux.org/alpine/v3.24"'))

-- layering effects reached the IR
assert(not out:find("cups", 1, true), "printing disabled by machine layer must"
       .. " vanish from packages AND actions")
assert(pos(out, '"timeout": 3') > 0, "machine layer timeout override")
assert(pos(out, '"shell": "/bin/bash"') > 0, "machine layer shell override")
assert(not out:find("/usr/bin/zsh", 1, true), "base shell gone")
assert(pos(out, '"bat"') and pos(out, '"dust"'), "user layer packages")
assert(pos(out, '"tlp"') and pos(out, '"unit": "tlp"'),
       "layer-introduced escape-hatch service enabled")
assert(pos(out, '"quiet"'), "machine layer kernel argument appended")

-- service rules from the backend registry
assert(pos(out, '"unit": "sshd"') and pos(out, '"runlevel": "boot"'))
assert(pos(out, '"settings_file": "/etc/ssh/sshd_config"'))
assert(pos(out, '"permit_root_login": false'))
assert(pos(out, '"unit": "tailscale"'), "escape hatch unit = service key")
assert(not out:find('"unit": "pipewire', 1, true),
       "backend service_name=false means packages only, never enabled")
assert(pos(out, '"pipewire-jack"') and pos(out, '"wireplumber"'),
       "backend baseline + additive config packages both installed")

-- storage plan must carry sizes: the executor cannot partition from names alone
assert(pos(out, '"size": "512MB"') and pos(out, '"type": "esp"')
       and pos(out, '"size": "100%"'),
       "partition_disk must carry index/name/size/type per partition")

-- home layer
assert(pos(out, '"op": "enable_home_service"')
       and pos(out, '"mechanism": "user-daemon"')
       and pos(out, '"user": "abuss"'))

-- networking (machine layer material): profiles emitted as an ordered,
-- settings-free action; psk travels ONLY as a secret reference
assert(pos(out, '"op": "configure_network"') > pos(out, '"op": "install_packages"')
       and pos(out, '"op": "configure_network"') < pos(out, '"op": "create_generation"'),
       "configure_network must sit between install and generation")
assert(pos(out, '"ssid": "homecloud"'))
assert(pos(out, '"secret": "wifi.home.psk"'), "psk travels as secret reference")
assert(not out:find('"psk": "', 1, true), "raw psk value is inexpressible")
local cs_count = 0
for _ in out:gmatch('"op": "configure_service"') do cs_count = cs_count + 1 end
assert(cs_count == 1, "only ssh gets configure_service; connections are not settings")

-- canonical ordering (IR.md): storage -> repos (ON the mounted root: the
-- repo tree + repos.d must survive the root mount) -> files -> install
-- -> generation -> activate -> users/services (they run INSIDE the
-- activated chroot) -> bootloader
local order = {
    '"op": "partition_disk"', '"op": "format"', '"op": "create_swap"',
    '"op": "mount"', '"op": "configure_repos"', '"op": "write_file"',
    '"op": "install_packages"', '"op": "create_generation"',
    '"op": "activate_generation"', '"op": "create_user"',
    '"op": "enable_service"', '"op": "install_bootloader"',
}
local last = 0
for _, s in ipairs(order) do
    local p = pos(out, s)
    assert(p > last, "out of order: " .. s)
    last = p
end

-- determinism: two runs byte-identical
local again = run("system.lua ../../example/eszkoz/layers/machine.lua ../../example/eszkoz/layers/user.lua")
assert(out == again, "generator output is not deterministic")

-- flatpak: repos.flatpak + {repo="flatpak"} tags -> backend tool package,
-- remote config, and system + per-user install actions (scratch layer)
local f2 = assert(io.open("_scratch_flatpak.lua", "w"))
f2:write([[
return {
    repos = { flatpak = { type = "flatpak", name = "flathub",
        url = "https://dl.flathub.org/repo/flathub.flatpakrepo" } },
    packages = { { repo = "flatpak", name = "org.gimp.GIMP" } },
    users = { abuss = { environment = { packages = {
        { repo = "flatpak", name = "com.spotify.Client" } } } } },
}
]])
f2:close()
local fp = run("system.lua", "_scratch_flatpak.lua")
assert(pos(fp, '"flatpak"') < pos(fp, '"op": "install_packages"'),
       "backend flatpak tool package must be in the install set")
assert(pos(fp, '"op": "install_packages"') < pos(fp, '"op": "configure_flatpak_remote"'),
       "remote is configured only after the tool exists")
assert(pos(fp, '"name": "flathub"') and pos(fp, "flathub.flatpakrepo"))
local s1, s2 = fp:find('"op": "install_flatpak"'), nil
s2 = fp:find('"op": "install_flatpak"', s1 + 1)
assert(s1 and s2, "system + per-user install_flatpak actions expected")
assert(pos(fp, '"org.gimp.GIMP"') > pos(fp, '"op": "install_packages"'),
       "system app installed after the tool")
assert(pos(fp, '"com.spotify.Client"') > pos(fp, '"org.gimp.GIMP"'),
       "user app action comes after the system one")
assert(fp:find('"user": "abuss"', s2), "per-user scope carries the login")
assert(not fp:sub(1, pos(fp, '"op": "install_packages"')):find("gimp", 1, true),
       "app ids must never enter the distro install set")
os.remove("_scratch_flatpak.lua")

-- ====== local repositories: configure_repos gains repos list ======
local f_lr = assert(io.open("_scratch_localrepo.lua", "w"))
f_lr:write([[
return { repositories = { mylocal = { url = "file:///kod/repo" } } }
]])
f_lr:close()
local lr = run("system.lua _scratch_localrepo.lua")
assert(lr:find('"repos"', 1, true), "configure_repos must carry repos list when repositories present")
assert(lr:find('"name": "mylocal"', 1, true), "repo name must appear in repos list")
assert(lr:find('"url": "file:///kod/repo"', 1, true), "repo url must appear in repos list")
os.remove("_scratch_localrepo.lua")

-- without repositories section: repos field absent (byte-identity)
assert(not base:find('"repos"', 1, true), "configure_repos must NOT carry repos field when no repositories")

-- ====== copy_tree: local repo with src emits copy_tree BEFORE configure_repos ======
local f_ct = assert(io.open("_scratch_copytree.lua", "w"))
f_ct:write([[
return { repositories = { myrepo = { url = "file:///kod/repo", src = "vm/repo" } } }
]])
f_ct:close()
local ct = run("system.lua _scratch_copytree.lua")
local ct_pos = ct:find('"op": "copy_tree"', 1, true)
local cr_pos = ct:find('"op": "configure_repos"', 1, true)
assert(ct_pos, "copy_tree op must be present when repository has src")
assert(ct_pos < cr_pos, "copy_tree must precede configure_repos")
-- storage-first contract (live-VM paste round 2026-11-14): copy_tree AND
-- configure_repos emit AFTER the LAST mount (repo tree + repos.d land on the
-- mounted root, never under it) and before the writes/install that consume them
local last_mount = 0
while true do
    local nxt = ct:find('"op": "mount"', last_mount + 1, true)
    if not nxt then break end
    last_mount = nxt
end
assert(last_mount > 0, "system.lua storage must emit mount ops")
assert(ct_pos > last_mount and cr_pos > last_mount,
       "copy_tree and configure_repos must follow the storage mounts")
assert(cr_pos < ct:find('"op": "write_file"', 1, true)
       and cr_pos < ct:find('"op": "install_packages"', 1, true),
       "configure_repos must precede write_file and install_packages")
assert(ct:find('"dst": "/kod/repo"', 1, true), "copy_tree dst must be url minus file://")
-- repos entry must carry src (host-abs) when copy_tree was declared
assert(ct:find('"src":', 1, true), "configure_repos repos entry must carry src when copy_tree declared")
-- src must be absolute (provenance resolved against layer dir = ".")
local src_m = ct:match('"src": "([^"]+)"')
assert(src_m and src_m:sub(1,1) == "/", "copy_tree src must be absolute, got: " .. tostring(src_m))
assert(src_m:find("vm/repo", 1, true), "copy_tree src must contain the relative path")
-- no copy_tree when src absent
assert(not lr:find('"op": "copy_tree"', 1, true), "copy_tree must NOT appear when src is absent")
os.remove("_scratch_copytree.lua")

-- ====== managed files: config `files` -> write_file ops (path/content/mode) ====
local f_ft = assert(io.open("_scratch_files.lua", "w"))
f_ft:write([[
return { files = {
    sec = { path = "/etc/kodos/secrets.json", content = "{}\n", mode = "0600" },
    cfg = { path = "/etc/kodos/config.lua", content = "return {}\n" },
} }
]])
f_ft:close()
local ft = run("system.lua _scratch_files.lua")
assert(ft:find('"path": "/etc/kodos/config.lua"', 1, true)
       and ft:find('"content": "return {}\\n"', 1, true),
       "a files entry must emit a write_file carrying its path + content")
assert(ft:find('"mode": "0600"', 1, true), "a files entry's mode must reach the action")
assert(ft:find('"path": "/etc/kodos/config.lua"', 1, true)
       < ft:find('"path": "/etc/kodos/secrets.json"', 1, true),
       "files entries emit sorted by name (determinism)")
-- write_file is rank 7: the machine's own config must exist before the
-- generation/user ops that assume it (IR.md order)
assert(ft:find('"path": "/etc/kodos/secrets.json"', 1, true)
       < ft:find('"op": "install_packages"', 1, true),
       "files writes must precede install_packages")
os.remove("_scratch_files.lua")

-- a service entry's stray keys are settings; without a backend settings file
-- they must abort GENERATION, naming the service (field-tested: this used to
-- surface only as an anonymous exec-time configure_service failure)
local f4 = assert(io.open("_scratch_svc.lua", "w"))
f4:write('return { services = { weirdsvc = { enable = true, typo_key = true } } }')
f4:close()
local err4
if os.getenv("KODOS_INPROCESS") == "1" then
    local ok, e = pcall(kodos_run, "generate.lua", "system.lua", "_scratch_svc.lua")
    err4 = ok and "" or tostring(e)
else
    local h4 = assert(io.popen((os.getenv("KODOS_LUA_CMD") or "lua") .. " generate.lua system.lua _scratch_svc.lua 2>&1"))
    err4 = h4:read("*a")
    h4:close()
end
os.remove("_scratch_svc.lua")
assert(err4:find("weirdsvc: settings", 1, true),
       "settings without a backend settings file must abort with the service name")

-- flatpak ids without repos.flatpak: hard error, same strictness rule
local f3 = assert(io.open("_scratch_flatpak2.lua", "w"))
f3:write('return { packages = { { repo = "flatpak", name = "org.gimp.GIMP" } } }')
f3:close()
local err2
if os.getenv("KODOS_INPROCESS") == "1" then
    local ok, e = pcall(kodos_run, "generate.lua", "system.lua", "_scratch_flatpak2.lua")
    err2 = ok and "" or tostring(e)
else
    local h2 = assert(io.popen((os.getenv("KODOS_LUA_CMD") or "lua") .. " generate.lua system.lua _scratch_flatpak2.lua 2>&1"))
    err2 = h2:read("*a")
    h2:close()
end
os.remove("_scratch_flatpak2.lua")
assert(err2:find("repos.flatpak is missing", 1, true),
       "flatpak packages without a declared remote must abort generation")

-- unofferable kernel variant aborts generation (no silent fallback)
local f = assert(io.open("_scratch.lua", "w"))
f:write('return { boot = { kernel = { variant = "hardened" } } }')
f:close()
local err
if os.getenv("KODOS_INPROCESS") == "1" then
    local ok, e = pcall(kodos_run, "generate.lua", "system.lua", "_scratch.lua")
    err = ok and "" or tostring(e)
else
    local h = assert(io.popen((os.getenv("KODOS_LUA_CMD") or "lua") .. " generate.lua system.lua _scratch.lua 2>&1"))
    err = h:read("*a")
    h:close()
end
os.remove("_scratch.lua")
assert(err:find("not offered by backend", 1, true),
       "kernel variant the backend cannot offer must be a hard error")

-- ====== --inplace: storage ops filtered, rest intact ======
-- Use the layered config which exercises partition_disk/format/create_swap.
local inplace_args = "system.lua ../../example/eszkoz/layers/machine.lua ../../example/eszkoz/layers/user.lua --inplace"
local ip
if os.getenv("KODOS_INPROCESS") == "1" then
    ip = kodos_run("generate.lua", "system.lua",
        "../../example/eszkoz/layers/machine.lua",
        "../../example/eszkoz/layers/user.lua", "--inplace")
else
    local cmd2 = (os.getenv("KODOS_LUA_CMD") or "lua") .. " generate.lua "
                 .. inplace_args .. " 2>/dev/null"
    local h2 = assert(io.popen(cmd2))
    ip = h2:read("*a")
    h2:close()
    assert(#ip > 100, "inplace generator produced no output")
end

assert(not ip:find('"op": "partition_disk"', 1, true),
    "--inplace must omit partition_disk ops")
assert(not ip:find('"op": "format"', 1, true),
    "--inplace must omit format ops")
assert(not ip:find('"op": "create_swap"', 1, true),
    "--inplace must omit create_swap ops")
assert(ip:find('"op": "mount"', 1, true),
    "--inplace must KEEP mount ops (skip-if-mounted guard fires on live machine)")
assert(ip:find('"op": "configure_repos"', 1, true)
       > ip:find('"op": "mount"', 1, true),
    "--inplace must keep configure_repos, still AFTER the mounts")
assert(ip:find('"op": "install_packages"', 1, true),
    "--inplace must keep install_packages")
assert(ip:find('"op": "create_generation"', 1, true),
    "--inplace must keep create_generation")
assert(ip:find('"op": "activate_generation"', 1, true),
    "--inplace must keep activate_generation")
assert(ip:find('"op": "install_bootloader"', 1, true),
    "--inplace must keep install_bootloader")

-- Without the flag, format is still present
assert(out:find('"op": "format"', 1, true),
    "without --inplace, format must still be present")

-- ====== arch backend + serial console: unit enable, no inittab ======
local f_ar = assert(io.open("_scratch_arch.lua", "w"))
f_ar:write([[
return {
    repos = { base = { type = "arch", mirror = "https://geo.mirror.pkgbuild.com",
                       branches = { "core", "extra" } } },
    boot = { bootloader = { type = "grub", efi = true, timeout = 5 } },
    system = { host_name = "archt", timezone = "UTC",
               locale = { default = "C.UTF-8" }, serial_console = true },
    generations = { storage = "store", root = "/kod", keep = 5 },
    storage = { devices = { disk0 = { device = "/dev/nbd0", type = "gpt", efi = true,
        partitions = {
            { name = "boot", size = "512MB", type = "esp", filesystem = "vfat",
              mountpoint = "/boot", check = false },
            { name = "root", size = "100%", type = "linux", filesystem = "ext4",
              mountpoint = "/" } } } } },
}
]])
f_ar:close()
local ar = run("_scratch_arch.lua")
assert(pos(ar, '"distro": "arch"'))
assert(pos(ar, '"unit": "serial-getty@ttyS0"') > pos(ar, '"op": "activate_generation"'),
       "serial console unit enables AFTER activation, like any unit")
assert(pos(ar, '"runlevel": "getty"'))
assert(not ar:find("/etc/inittab", 1, true),
       "systemd backend must never render the OpenRC inittab")
assert(pos(ar, '"linux-firmware"') and pos(ar, '"base"'))
os.remove("_scratch_arch.lua")

print("PASS: IR stream (layered config, ordered, deterministic, strict)")
