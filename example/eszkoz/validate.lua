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
            elseif v.repo ~= "base" and v.repo ~= "flatpak" then
                err("unknown repo '" .. tostring(v.repo) .. "' (base|flatpak)")
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
        } },

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
            storage = "string", root = "string", keep = "number",
        } },

        system = { record = {
            host_name = "string",
            locale = { record = { default = "string" } },
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
    },
}

-- returns a list of error strings (empty = config is schema-valid)
function M.validate(conf)
    local errs = {}
    check(conf, M.schema, "$", errs)
    -- cross-field rules the shape checker cannot express
    for sname, s in pairs(conf.services or {}) do
        if type(s) == "table" then
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
