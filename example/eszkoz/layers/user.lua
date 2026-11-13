-- layers/user.lua — USER layer overlay (home-level additions for abuss).
-- Merged last: machine choices lose to the user's own on conflicts.
return {
    users = {
        abuss = {
            environment = {
                -- appended to the home package list (canonical, v3.24-verified)
                packages = { { repo = "base", name = "bat" },
                             { repo = "base", name = "dust" } },
                -- deep dict merge: extends git config, keeps the rest
                config = { git = { alias = { st = "status" } } },
            },
        },
    },
}
