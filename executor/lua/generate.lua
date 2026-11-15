-- generate.lua — reference Lua generator: merged config (+layers) + backend
-- -> IR action stream (contract: IR.md). Pure data transformation: it
-- decides WHAT and in what ORDER; the compiled executor decides HOW.
-- Usage: lua generate.lua <config.lua> [layer.lua ...] > actions.json
local resolve = require("resolve")
local validate = require("validate")
local json = require("json")

-- Strip --inplace from args before config loading
local inplace = false
local filtered_args = {}
for _, v in ipairs(arg) do
    if v == "--inplace" then inplace = true
    else filtered_args[#filtered_args + 1] = v end
end
arg = filtered_args

-- Track provenance: for each layer file, capture its own repositories table
-- (before merge) so we can resolve relative src paths against that layer's dir.
-- Last declaration of a name wins (mirrors the merge order).
local repo_src_provenance = {} -- name -> absolute src path
do
    -- capture cwd once; layer paths relative to it or to their own dir
    -- ponytail: dofile re-executes each layer; layers assumed pure data (return table only); revisit if layers ever acquire side effects
    local cwd_h = io.popen("pwd")
    local cwd = cwd_h:read("*l")
    cwd_h:close()
    for _, p in ipairs(arg) do
        local layer = dofile(p)
        if type(layer) == "table" and type(layer.repositories) == "table" then
            local layer_dir = p:match("^(.*)/[^/]+$") or "."
            if layer_dir:sub(1,1) ~= "/" then
                layer_dir = cwd .. "/" .. layer_dir
            end
            for name, entry in pairs(layer.repositories) do
                if type(entry) == "table" and entry.src then
                    local src = entry.src
                    if src:sub(1, 1) ~= "/" then
                        src = layer_dir .. "/" .. src
                    end
                    repo_src_provenance[name] = src
                end
            end
        end
    end
end

local conf = resolve.load(arg)

-- schema-validate the MERGED config before anything generates
local serrs = validate.validate(conf)
if #serrs > 0 then
    for _, e in ipairs(serrs) do io.stderr:write("SCHEMA ERROR: " .. e .. "\n") end
    os.exit(1)
end

local backend = resolve.backend_for(conf)

local inplace_skip = { partition_disk = true, format = true, create_swap = true }
local actions = {}
local function act(op, f)
    if inplace and inplace_skip[op] then return end
    f = f or {}
    f.op = op
    actions[#actions + 1] = f
end

-- ====== 1-4. storage: partition, format, swap, mount ======
local dnames = {}
for k in pairs(conf.storage.devices) do dnames[#dnames + 1] = k end
table.sort(dnames)
local mounts, swaps = {}, {}
for _, dn in ipairs(dnames) do
    local d = conf.storage.devices[dn]
    local parts = {}
    for i, p in ipairs(d.partitions) do
        parts[i] = { index = i, name = p.name, size = p.size, type = p.type }
    end
    act("partition_disk", { device = d.device, table = d.type, efi = d.efi,
                            partitions = parts })
    for i, p in ipairs(d.partitions) do
        -- nvme/mmcblk style disks number partitions with a 'p' separator
        local part = d.device .. (d.device:match("%d$") and "p" or "") .. i
        if p.type == "linux-swap" then
            act("create_swap", { partition = part })
            swaps[#swaps + 1] = { part = part, point = "none", fs = "swap",
                                  options = "sw", pass = 0 }
        else
            act("format", { partition = part, filesystem = p.filesystem })
            if p.mountpoint then
                mounts[#mounts + 1] = { part = part, point = p.mountpoint,
                    fs = p.filesystem, options = p.options or "defaults",
                    pass = p.check == false and 0
                           or (p.mountpoint == "/" and 1 or 2) }
            end
        end
    end
end
-- mount root before children (shortest path first)
table.sort(mounts, function(a, b)
    return #a.point < #b.point or (#a.point == #b.point and a.point < b.point)
end)
for _, m in ipairs(mounts) do
    act("mount", { partition = m.part, point = m.point, options = m.options })
end

-- ====== 5. repos: copy_tree* + configure_repos, AFTER the storage mounts ======
-- Both write INTO the target root (<root>/kod/repo, <root>/etc/apk/
-- repositories + repos.d): at bootstrap they must land on the MOUNTED root,
-- otherwise the root mount covers whatever they wrote into the unmounted
-- root dir (live-VM paste round 2026-11-14: repo tree + repos.d vanished
-- under the fresh /; the scratch tmpfs pre-mounts masked this). One copy_tree
-- per local repo entry that declares src; copy_tree (#5) stays BEFORE
-- configure_repos (#6) so the repo tree exists before apk resolves it.
local configure_repos_act = {
    type = conf.repos.base.type,
    mirror = conf.repos.base.mirror,
    branches = conf.repos.base.branches,
}
if conf.repositories then
    local rnames = {}
    for n in pairs(conf.repositories) do rnames[#rnames + 1] = n end
    table.sort(rnames)
    if #rnames > 0 then
        local repos_list = {}
        for _, n in ipairs(rnames) do
            local entry = { name = n, url = conf.repositories[n].url }
            -- emit copy_tree before configure_repos if src was declared
            if repo_src_provenance[n] then
                local url = conf.repositories[n].url
                local dst = url:gsub("^file://", "")
                act("copy_tree", { name = n, src = repo_src_provenance[n], dst = dst })
                entry.src = repo_src_provenance[n] -- host-abs path for executor apk resolution
            end
            repos_list[#repos_list + 1] = entry
        end
        configure_repos_act.repos = repos_list
    end
end
act("configure_repos", configure_repos_act)

-- ====== 7. derived files: fstab, hostname ======
-- Device names are install-time identities (nbd under the installer); the
-- executor expands {UUID:<device>} placeholders via blkid at write_file
-- time, so the guest fstab uses stable UUID= sources.
local fstab = {}
for _, m in ipairs(mounts) do
    fstab[#fstab + 1] = table.concat(
        { "{UUID:" .. m.part .. "}", m.point, m.fs, m.options, 0, m.pass }, "\t")
end
for _, s in ipairs(swaps) do
    fstab[#fstab + 1] = table.concat(
        { "{UUID:" .. s.part .. "}", s.point, s.fs, s.options, 0, 0 }, "\t")
end
act("write_file", { path = "/etc/fstab",
                    content = table.concat(fstab, "\n") .. "\n" })
act("write_file", { path = "/etc/hostname",
                    content = conf.system.host_name .. "\n" })

-- serial console: backend-owned inittab (alpine baselayout ships the
-- getty line COMMENTED OUT, so OpenRC boots fine but nothing ever reads
-- the serial line: silent boot ending with no login prompt).
if conf.system.serial_console then
    if not backend.inittab then
        error("system.serial_console: backend '" .. backend.name ..
              "' has no serial getty support")
    end
    act("write_file", { path = "/etc/inittab", content = backend.inittab })
end

-- sudoers: Alpine's sudo ships the %wheel rule COMMENTED OUT (upstream
-- default) and an empty sudoers.d; enabling it is the admin's job on a
-- vanilla box. Our user model already uses wheel membership as admin,
-- so declare the policy as a managed drop-in (sudoers.d is @includedir
--ed by the stock sudoers).
act("write_file", { path = "/etc/sudoers.d/60-kodos-admin",
                    content = "# kodos: wheel members are administrators\n" ..
                              "%wheel ALL=(ALL:ALL) ALL\n" })

-- ====== 7b. managed files: the config's `files` section -> write_file ops ======
-- name -> {path, content, mode?}; sorted by name for determinism. This is how
-- a config ships files no package owns (Task 13: the machine's own
-- /etc/kodos/config.lua + secrets sidecar, so `kodos apply` has its config
-- ON DISK — the toolchain is embedded in the binary, the config is not).
if conf.files then
    local fnames = {}
    for n in pairs(conf.files) do fnames[#fnames + 1] = n end
    table.sort(fnames)
    for _, n in ipairs(fnames) do
        local f = conf.files[n]
        local a = { path = f.path, content = f.content }
        if f.mode then a.mode = f.mode end
        act("write_file", a)
    end
end

-- ====== 8. packages: one deduped install set, sorted for determinism ======
-- (versions come from the lockfile / generation-N.json, see lockfile.lua;
-- the executor installs EXACTLY those versions of these names)
local plan = resolve.service_plan(conf, backend)
local hplan = resolve.home_service_plan(conf, backend)
local all, dropped = resolve.install_set(conf, backend)
act("install_packages", { names = all })

-- ====== 9-10. flatpak (needs the flatpak tool, so after install_packages;
-- app ids are Flathub-native: no backend mapping involved) ======
local fp = resolve.flatpak_plan(conf)
if (#fp.system > 0 or #fp.users > 0) and not fp.remote then
    error("flatpak packages declared but repos.flatpak is missing")
end
if fp.remote then
    if fp.remote.type ~= "flatpak" then
        error("repos.flatpak.type must be 'flatpak', got '" .. fp.remote.type .. "'")
    end
    act("configure_flatpak_remote", { name = fp.remote.name, url = fp.remote.url })
end
if #fp.system > 0 then
    act("install_flatpak", { ids = fp.system })
end
for _, scope in ipairs(fp.users) do
    act("install_flatpak", { user = scope.user, ids = scope.ids })
end

-- ====== 11-16. system settings + service settings ======
if conf.system.locale then
    act("configure_locale", { default = conf.system.locale.default })
end
if conf.system.keyboard_layout then
    act("configure_keyboard", {
        layout = conf.system.keyboard_layout.layout,
        console_keymap = conf.system.console_keymap,
    })
end
if conf.system.timezone then
    act("configure_timezone", { timezone = conf.system.timezone })
end
local kern = conf.boot.kernel or {}
if kern.modules then act("load_kernel_modules", { modules = kern.modules }) end
if kern.arguments then act("set_kernel_arguments", { arguments = kern.arguments }) end
for _, s in ipairs(plan) do
    if s.settings then
        act("configure_service", { name = s.name, settings = s.settings,
                                   settings_file = s.settings_file })
    end
end

-- ====== networking: connection profiles. psk is a SECRET REFERENCE
-- ({secret="dotted.key"}); raw values can never appear here -- the
-- executor resolves refs from its out-of-band secrets store ======
local nets = {}
for sname, srv in pairs(conf.services or {}) do
    if srv.enable and srv.connections then
        for cname, c in pairs(srv.connections) do
            nets[#nets + 1] = { service = sname, name = cname, type = c.type,
                                ssid = c.ssid, psk = c.psk }
        end
    end
end
table.sort(nets, function(a, b)
    return a.service < b.service
        or (a.service == b.service and a.name < b.name)
end)
if #nets > 0 then act("configure_network", { connections = nets }) end

-- ====== 18-19. generation + activation (users/services run INSIDE the chroot:
-- busybox + init.d scripts only exist once the generation is activated,
-- so this must precede them) ======
act("create_generation", { packages = all, root = conf.generations.root,
                           generations_keep = conf.generations.keep or 5 })
act("activate_generation", { root = conf.generations.root })

-- users (dict keyed by username; emit sorted for determinism)
for _, n in ipairs(resolve.user_names(conf)) do
    local u = conf.users[n]
    act("create_user", {
        user = n, name = u.name, group = u.group,
        supplementary_groups = u.supplementary_groups,
        home_directory = u.home_directory, shell = u.shell,
        create_home = u.create_home,
    })
end

-- services + home services, then bootloader last (fstab + kernel args
-- are in place by then)
for _, s in ipairs(plan) do
    if s.unit then
        act("enable_service", { name = s.name, unit = s.unit,
                                runlevel = s.runlevel })
    end
    for _, x in ipairs(s.extra or {}) do
        act("enable_service", { name = s.name, unit = x.unit,
                                runlevel = x.runlevel })
    end
end
for _, s in ipairs(hplan) do
    act("enable_home_service", { user = s.user, name = s.name,
                                 mechanism = s.mechanism, packages = s.packages,
                                 settings = s.settings })
end
local bl = conf.boot.bootloader
act("install_bootloader", { type = bl.type, efi = bl.efi, timeout = bl.timeout })

-- ====== emit JSON (deterministic: json.lua sorts dict keys) ======
if #dropped > 0 then
    io.stderr:write("dropped by backend: " .. table.concat(dropped, ", ") .. "\n")
end
print(json.encode({ schema_version = 1, distro = backend.name, actions = actions }))
