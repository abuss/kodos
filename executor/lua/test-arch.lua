-- test-arch.lua — verify Arch backend mappings
local backend = require('executor/lua/backends/arch')

-- Test: Arch backend maps display_manager to correct package
local display_managers = {
    { name = "gdm", expected = "gnome-shell" },
    { name = "sddm", expected = "sddm" },
    { name = "lightdm", expected = "lightdm" },
    { name = "cosmic-greeter", expected = "cosmic-greeter" }
}

for _, dm in ipairs(display_managers) do
    local pkg = backend.map[dm.name]
    assert(pkg == dm.expected, 
        "Arch backend map['" .. dm.name .. "'] should be '" .. dm.expected .. "', got " .. tostring(pkg))
end

-- install_bootloader runs mkinitcpio in the activated root; 2026-11 Arch's
-- `base` %DEPENDS% no longer ships it, so the backend must request it.
local has = false
for _, p in ipairs(backend.base_packages) do
    if p == "mkinitcpio" then has = true break end
end
assert(has, "base_packages must include mkinitcpio: install_bootloader runs it in-chroot")

print("✓ All Arch display_manager mappings verified")
