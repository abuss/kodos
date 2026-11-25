-- validate.lua — schema validation for the eszkoz dialect.
-- Runs on the MERGED config before anything generates: typo'd sections,
-- unknown keys, wrong types and missing required fields become errors
-- here, not silently-wrong actions downstream.
-- Pure schema: backend knowledge (package existence, units, locale sets,
-- kernel variants, escape hatches) stays in check-packages.lua.
local resolve = require("resolve")
local is_array = resolve.is_array
local M = {}

local function check(v, spec, path, errs)
    local function err(msg) errs[#errs + 1] = path .. ": " .. msg end

    if type(spec) == "string" then
        if spec == "any" then
            return
        elseif spec == "tagged" then
            if type(v) ~= "table" then
                err("expected tagged package {repo=..., name=...}, got " .. type(v))
            elseif type(v.name) ~= "string" or v.name == "" then
                err("tagged package entry needs a name")
            elseif type(v.repo) ~= "string" or v.repo == "" then
                err("tagged package entry needs a repo")
            end
        elseif spec == "str_or_false" then
            if type(v) ~= "string" and v ~= false then
                err("expected string or false, got " .. type(v))
            end
        else
            local want = { string = "string", bool = "boolean", number = "number" }
            if type(v) ~= want[spec] then
                err("expected " .. spec .. ", got " .. type(v))
            end
        end
        return
    end

    if spec.optional then check(v, spec.optional, path, errs) return end

    if spec.enum then
        if type(v) ~= "string" then
            err("expected string, got " .. type(v))
        else
            for _, e in ipairs(spec.enum) do
                if v == e then return end
            end
            err("must be one of: " .. table.concat(spec.enum, ", "))
        end
    elseif spec.record then
        if type(v) ~= "table" or is_array(v) then
            err("expected dict, got " .. (type(v) ~= "table" and type(v) or "list"))
            return
        end
        for k, fs in pairs(spec.record) do
            if v[k] == nil then
                if not (type(fs) == "table" and fs.optional) then
                    errs[#errs + 1] = path .. "." .. k .. ": missing required field"
                end
            else
                check(v[k], fs, path .. "." .. k, errs)
            end
        end
        for k in pairs(v) do
            if spec.record[k] == nil then
                if spec.extra == "scalar" then
                    local t = type(v[k])
                    if t ~= "string" and t ~= "number" and t ~= "boolean" then
                        errs[#errs + 1] = path .. "." .. k
                            .. ": service settings values must be scalar"
                    end
                else
                    errs[#errs + 1] = path .. "." .. k .. ": unknown key (typo?)"
                end
            end
        end
    elseif spec.dict_of then
        if type(v) ~= "table" or is_array(v) then
            err("expected dict, got " .. (type(v) ~= "table" and type(v) or "list"))
            return
        end
        for k, val in pairs(v) do
            check(val, spec.dict_of, path .. "." .. k, errs)
        end
    elseif spec.array_of then
        if not is_array(v) then
            err("expected list, got " .. (type(v) ~= "table" and type(v) or "dict"))
            return
        end
        for i, val in ipairs(v) do
            check(val, spec.array_of, path .. "[" .. i .. "]", errs)
        end
    end
end

-- ====== the schema of the dialect ======
local strlist = { array_of = "string" }
local tagged_list = { array_of = "tagged" }

-- system services: enable required (NixOS-style); connections is the
-- networking section (see conn_spec); anything not a known key is a
-- settings value and must be scalar (the settings writer's problem).
-- Honest limit: a typo'd settings key (enabkle=true) is
-- indistinguishable from a real settings key at schema level -- catching
-- that needs per-service settings knowledge, which lives in the backend.
local service_spec = {
    record = {
        enable = "bool",
        packages = { optional = tagged_list },
        service_name = { optional = "str_or_false" },
        connections = { optional = { dict_of = { record = {
            type = { enum = { "wifi", "ethernet" } },
            ssid = { optional = "string" },
            -- psk MUST be a secret reference: a literal string fails this
            -- check, so raw secrets can never enter config or the IR.
            psk = { optional = { record = { secret = "string" } } },
        } } } },
    },
    extra = "scalar",
}

M.schema = {
    record = {
        repos = { record = {
            base = { record = {
                type = { enum = { "apk", "arch", "deb" } },
                mirror = "string",
                branches = { optional = strlist },
            } },
            flatpak = { optional = { record = {
                type = { enum = { "flatpak" } },
                name = "string", url = "string",
            } } },
            aur = { optional = { record = {
                type = { enum = { "aur" } },
                helper = "string"
            } } },
        } },

        -- optional local file:// repos; keyed by repo name, each entry {url=...}.
        -- Same dict_of override semantics as all other sections: a later layer
        -- with the same repo name overwrites the earlier entry (last-write-wins).
        repositories = { optional = { dict_of = { record = { url = "string", src = { optional = "string" } } } } },

        boot = { record = {
            bootloader = { record = {
                type = "string", efi = "bool", timeout = "number",
            } },
            kernel = { optional = { record = {
                variant = { optional = "string" },
                modules = { optional = strlist },
                arguments = { optional = strlist },
            } } },
        } },

        storage = { record = { devices = { dict_of = { record = {
            device = "string", type = "string",
            efi = { optional = "bool" },
            partitions = { array_of = { record = {
                name = "string", size = "string", type = "string",
                filesystem = "string",
                mountpoint = { optional = "string" },
                options = { optional = "string" },
                check = { optional = "bool" },
            } } },
        } } } } },

        generations = { record = {
            storage = "string", root = "string", keep = { optional = "number" },
        } },

        system = { record = {
            host_name = "string",
            locale = { record = {
                default = "string",
                extra = { optional = strlist },
            } },
            timezone = { optional = "string" },
            keyboard_layout = { optional = { record = { layout = "string" } } },
            console_keymap = { optional = "string" },
            serial_console = { optional = "bool" },
        } },

        services = { optional = { dict_of = service_spec } },

        users = { optional = { dict_of = { record = {
            name = "string", group = "string",
            supplementary_groups = { optional = strlist },
            home_directory = { optional = "string" },
            shell = { optional = "string" },
            create_home = { optional = "bool" },
            no_password = { optional = "bool" },
            environment = { optional = { record = {
                -- AUR packages: {repo="aur", name="pkg"} supported in user environment only.
                -- System-level AUR support TBD (requires backend-specific tooling).
                packages = { optional = tagged_list },
                config = { optional = "any" },   -- free-form per-writer
                dotfiles = { optional = "any" },
                services = { optional = { dict_of = { record = {
                    enable = "bool",
                    packages = { optional = tagged_list },
                    settings = { optional = "any" },
                } } } },
            } } },
        } } } },

        -- top-level packages: FLAT tagged list (all-packages.lua keeps
        -- authoring categories internally and exports the flat .all)
        packages = { optional = tagged_list },

        -- managed single-file writes: name -> {path, content, mode?}.
        -- Emitted as generic write_file ops (IR #6); mode is an OCTAL STRING
        -- ("0600"), absent = the executor's default 0644.
        files = { optional = { dict_of = { record = {
            path = "string", content = "string",
            mode = { optional = "string" },
        } } } },

        -- optional desktop configuration: display manager and enabled desktops
        desktop = { optional = { record = {
            display_manager = "string",  -- e.g. "gdm", "sddm", "lightdm", "cosmic-greeter"
            desktops = { optional = { dict_of = { record = {
                enable = "bool",
                extra_packages = { optional = tagged_list },
            } } } },
        } } },
    },
}

-- returns a list of error strings (empty = config is schema-valid)
function M.validate(conf)
    local errs = {}
    check(conf, M.schema, "$", errs)
    -- cross-field rules the shape checker cannot express
    for rname, r in pairs(conf.repositories or {}) do
        if type(r) == "table" and type(r.url) == "string"
           and not r.url:match("^file:///") then
            errs[#errs + 1] = "$.repositories." .. rname
                .. ": url must start with file:/// (got " .. r.url .. ")"
        end
    end
    -- tagged package repo values must name a known repo key
    local known_repos = { base = true, flatpak = true, aur = true }
    for rname in pairs(conf.repositories or {}) do known_repos[rname] = true end
    local function walk_tagged(t, path)
        if type(t) ~= "table" then return end
        if t.repo and t.name then
            if not known_repos[t.repo] then
                errs[#errs + 1] = path .. ": unknown repo '" .. tostring(t.repo)
                    .. "' (must be base, flatpak, or a key of repositories)"
            end
        else
            for k, v in pairs(t) do walk_tagged(v, path .. "." .. k) end
        end
    end
    walk_tagged(conf.packages or {}, "$.packages")
    -- generations.keep must be >= 1 if present
    if conf.generations and type(conf.generations.keep) == "number" then
        if conf.generations.keep < 1 then
            errs[#errs + 1] = "$.generations.keep: must be >= 1 (got " .. conf.generations.keep .. ")"
        end
    end
    -- files entries: write_file paths are target-root-absolute (IR.md), and a
    -- mode the executor cannot parse would abort a live stream at exec time
    for fname, f in pairs(conf.files or {}) do
        if type(f) == "table" then
            if type(f.path) == "string" and f.path:sub(1, 1) ~= "/" then
                errs[#errs + 1] = "$.files." .. fname
                    .. ": path must be absolute (got " .. f.path .. ")"
            end
            if f.mode ~= nil and not tostring(f.mode):match("^0?[0-7][0-7][0-7][0-7]?$") then
                errs[#errs + 1] = "$.files." .. fname
                    .. ': mode must be an octal string, e.g. "0600" (got '
                    .. tostring(f.mode) .. ")"
            end
        end
    end
    for uname, u in pairs(conf.users or {}) do
        walk_tagged((u.environment or {}).packages or {}, "$.users." .. uname .. ".environment.packages")
        for sname, s in pairs((u.environment or {}).services or {}) do
            walk_tagged((s.packages or {}), "$.users." .. uname .. ".environment.services." .. sname .. ".packages")
        end
    end
    for sname, s in pairs(conf.services or {}) do
        if type(s) == "table" then
            walk_tagged(s.packages or {}, "$.services." .. sname .. ".packages")
            for cname, c in pairs(s.connections or {}) do
                if type(c) == "table" and c.type == "wifi"
                   and not (type(c.psk) == "table" and c.psk.secret) then
                    errs[#errs + 1] = "$.services." .. sname .. ".connections."
                        .. cname .. ": wifi needs psk = { secret = \"key.ref\" }"
                end
            end
        end
    end
    return errs
end

return M
