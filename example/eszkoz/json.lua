-- json.lua — minimal deterministic JSON writer: dict keys sorted, so
-- output is byte-stable across runs (lockfiles must diff cleanly).
local resolve = require("resolve")
local M = {}

local function esc(s)
    return (s:gsub('[%c\\"]', { ['\\'] = '\\\\', ['"'] = '\\"',
                               ['\n'] = '\\n', ['\t'] = '\\t', ['\r'] = '\\r' }))
end

function M.encode(v, ind)
    ind = ind or ""
    local ni = ind .. "  "
    if type(v) == "table" then
        if resolve.is_array(v) then
            if #v == 0 then return "[]" end
            local parts = {}
            for _, x in ipairs(v) do parts[#parts + 1] = ni .. M.encode(x, ni) end
            return "[\n" .. table.concat(parts, ",\n") .. "\n" .. ind .. "]"
        end
        local keys = {}
        for k in pairs(v) do keys[#keys + 1] = tostring(k) end
        table.sort(keys)
        if #keys == 0 then return "{}" end
        local parts = {}
        for _, k in ipairs(keys) do
            parts[#parts + 1] = ni .. '"' .. esc(k) .. '": ' .. M.encode(v[k], ni)
        end
        return "{\n" .. table.concat(parts, ",\n") .. "\n" .. ind .. "}"
    elseif type(v) == "string" then
        return '"' .. esc(v) .. '"'
    end
    return tostring(v)
end

return M
