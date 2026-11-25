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

print("✓ All Arch display_manager mappings verified")
