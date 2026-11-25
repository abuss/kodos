-- Shared helpers for the modular eszkoz configs.
-- Distro-neutral dialect: packages are tagged {repo="base", name=...} by
-- canonical name; services are plain data keyed by neutral name (no helper
-- needed). All distro specifics live in backends/<distro>.lua.
local M = {}

-- extend: concatenate arrays (package lists)
function M.extend(...)
    local out = {}
    for _, t in ipairs({ ... }) do
        for _, v in ipairs(t) do out[#out + 1] = v end
    end
    return out
end

-- tagged package list: { repo = "base", name = "openssh" }
-- "base" = the distro's primary repo (repos.base); the backend maps the
-- canonical name to the distro name. Shape understood by proc-lua-conf
-- src/extract.lua.
local function tagged(repo, ...)
    local out = {}
    for _, n in ipairs({ ... }) do out[#out + 1] = { repo = repo, name = n } end
    return out
end

function M.base(...) return tagged("base", ...) end

-- flatpak app list: same tagged shape, repo = "flatpak" (Flathub ids are
-- already distro-neutral; declare repos.flatpak to use these)
function M.flatpak(...) return tagged("flatpak", ...) end

-- AUR package list: same tagged shape, repo = "aur" (Arch User Repository)
-- Arch-only; dropped by other backends
function M.aur(...) return tagged("aur", ...) end

return M
