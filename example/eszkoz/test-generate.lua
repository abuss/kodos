-- test-generate.lua — pin the IR contract: run generate.lua (base and
-- layered) and assert shape, order, and layering effects. Run: lua test-generate.lua
local function run(...)
    local h = assert(io.popen("lua generate.lua " .. table.concat({ ... }, " ")
                              .. " 2>/dev/null"))
    local out = h:read("a")
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

-- ====== layered: machine + user overlays change exactly what they say ======
local out = run("system.lua layers/machine.lua layers/user.lua")

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

-- canonical ordering (IR.md): storage -> install -> generation -> activate
-- -> users/services (they run INSIDE the activated chroot) -> bootloader
local order = {
    '"op": "configure_repos"', '"op": "partition_disk"', '"op": "format"',
    '"op": "mount"', '"op": "write_file"', '"op": "install_packages"',
    '"op": "create_generation"', '"op": "activate_generation"',
    '"op": "create_user"', '"op": "enable_service"',
    '"op": "install_bootloader"',
}
local last = 0
for _, s in ipairs(order) do
    local p = pos(out, s)
    assert(p > last, "out of order: " .. s)
    last = p
end

-- determinism: two runs byte-identical
local again = run("system.lua layers/machine.lua layers/user.lua")
assert(out == again, "generator output is not deterministic")

-- flatpak: repos.flatpak + {repo="flatpak"} tags -> backend tool package,
-- remote config, and system + per-user install actions (scratch layer)
local f2 = assert(io.open("layers/_scratch_flatpak.lua", "w"))
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
local fp = run("system.lua", "layers/_scratch_flatpak.lua")
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
os.remove("layers/_scratch_flatpak.lua")

-- flatpak ids without repos.flatpak: hard error, same strictness rule
local f3 = assert(io.open("layers/_scratch_flatpak2.lua", "w"))
f3:write('return { packages = { { repo = "flatpak", name = "org.gimp.GIMP" } } }')
f3:close()
local h2 = assert(io.popen("lua generate.lua system.lua layers/_scratch_flatpak2.lua 2>&1"))
local err2 = h2:read("a")
h2:close()
os.remove("layers/_scratch_flatpak2.lua")
assert(err2:find("repos.flatpak is missing", 1, true),
       "flatpak packages without a declared remote must abort generation")

-- unofferable kernel variant aborts generation (no silent fallback)
local f = assert(io.open("layers/_scratch.lua", "w"))
f:write('return { boot = { kernel = { variant = "hardened" } } }')
f:close()
local h = assert(io.popen("lua generate.lua system.lua layers/_scratch.lua 2>&1"))
local err = h:read("a")
h:close()
os.remove("layers/_scratch.lua")
assert(err:find("not offered by backend", 1, true),
       "kernel variant the backend cannot offer must be a hard error")

print("PASS: IR stream (layered config, ordered, deterministic, strict)")
