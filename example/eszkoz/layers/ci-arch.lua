-- ci-arch.lua — CI-only override: same config, Arch backend.
-- Usage: lua check-packages.lua arch.pkgs arch.pkgs \
--     system.lua layers/ci-arch.lua layers/machine.lua layers/user.lua
return { repos = { base = { type = "arch" } } }
