-- ci-deb.lua — CI-only override: same config, Debian (trixie) backend.
-- Usage: lua check-packages.lua deb.all deb.all \
--     system.lua layers/ci-deb.lua layers/machine.lua layers/user.lua
return { repos = { base = { type = "deb" } } }
