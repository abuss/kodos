-- alpm.lua — ALPM sync-DB (arch) index parse + dependency closure: the arch
-- counterpart of lockfile.lua's apk-index reading + apk-tools simulate. Pure
-- data (no popen/os.execute): runs under lua and gopher-lua alike.
-- desc format: %FIELD% header line, value lines until a blank line; records
-- start at %FILENAME% (or the first field at EOF).
local M = {}

function M.is_alpm(text)
    return text:find("\n%CHKSUM%\n", 1, true) ~= nil
        or text:find("\n%DEPENDS%\n", 1, true) ~= nil
end

function M.parse(text, repo)
    local into, cur = {}, {}
    local field, values
    local function commit()
        if field then cur[field] = values field, values = nil, nil end
        if cur.NAME then
            -- %FILENAME% = <repo>/os/<dirarch>/<name>-<ver>-<pkgarch>.pkg.tar.zst:
            -- the tarball's OWN arch component (x86_64 | any) is what the
            -- download URL needs (noarch pkgs still live under os/<dirarch>/).
            local fn = cur.FILENAME and cur.FILENAME[1]
            into[cur.NAME[1]] = {
                name = cur.NAME[1],
                version = cur.VERSION and cur.VERSION[1],
                -- real sync DBs carry %SHA256SUM%; %CHKSUM% is the older
                -- short form (and what our fixtures use). Either becomes
                -- the pin checksum the executor verifies against.
                checksum = (cur.CHKSUM and cur.CHKSUM[1])
                    or (cur.SHA256SUM and cur.SHA256SUM[1]),
                repo = repo,
                arch = fn and fn:match("%-([%w_]+)%.pkg%.tar%.zst$"),
                depends = cur.DEPENDS or {},
                provides = cur.PROVIDES or {},
            }
        end
        cur = {}
    end
    for line in (text .. "\n\n"):gmatch("([^\n]*)\n") do
        if line == "" then
            if field then cur[field] = values field, values = nil, nil end
        else
            local f = line:match("^%%([A-Z0-9]+)%%$")
            if f then
                if f == "FILENAME" then commit() end
                if field then cur[field] = values end
                field, values = f, {}
            elseif values then
                values[#values + 1] = line
            end
        end
    end
    commit()
    return into
end

-- vercmp: alpm/rpm semantics. [epoch:]pkgver[-pkgrel]; segment-wise compare
-- (digit runs numerically, alpha lexicographically, digits rank above alpha).
local function splitver(v)
    -- parens: gopher-lua turns a bare trailing match() into zero args for
    -- tonumber(); (match()) always passes exactly one value.
    local epoch = tonumber((v:match("^(%d+):"))) or 0
    local rest = v:gsub("^%d+:", "", 1)
    local pkgver, pkgrel = rest:match("^(.*)%-([^-]*)$")
    return epoch, pkgver or rest, pkgrel or "0"
end

local function cls(c)
    if c:match("%d") then return 2 elseif c:match("%a") then return 1 else return 0 end
end

local function rpmvercmp(a, b)
    local i, j = 1, 1
    while i <= #a and j <= #b do
        while i <= #a and cls(a:sub(i, i)) == 0 do i = i + 1 end
        while j <= #b and cls(b:sub(j, j)) == 0 do j = j + 1 end
        if i > #a or j > #b then break end
        local ca, cb = cls(a:sub(i, i)), cls(b:sub(j, j))
        if ca ~= cb then return ca > cb and 1 or -1 end
        local k, l = i, j
        while k <= #a and cls(a:sub(k, k)) == ca do k = k + 1 end
        while l <= #b and cls(b:sub(l, l)) == ca do l = l + 1 end
        local sa, sb = a:sub(i, k - 1), b:sub(j, l - 1)
        if ca == 2 then sa = sa:gsub("^0+", "") sb = sb:gsub("^0+", "") end
        if #sa ~= #sb then return #sa < #sb and -1 or 1 end
        if sa ~= sb then return sa < sb and -1 or 1 end
        i, j = k, l
    end
    if i <= #a then return 1 elseif j <= #b then return -1 else return 0 end
end

function M.vercmp(a, b)
    local ea, va, ra = splitver(a)
    local eb, vb, rb = splitver(b)
    if ea ~= eb then return ea < eb and -1 or 1 end
    local r = rpmvercmp(va, vb)
    if r ~= 0 then return r end
    return rpmvercmp(ra, rb)
end

local function dep_split(d)
    local n, op, want = d:match("^([^=<>]+)([=<>]+)(.+)$")
    if n then return n, op, want end
    return d, nil, nil
end

-- returns (found, version): found is the provider-match signal; version
-- may be nil for a versionless PROVIDES entry (real arch: bash provides "sh").
local function provide_version(rec, dn)
    for _, p in ipairs(rec.provides) do
        local pn, pv = p:match("^([^=]+)=(.+)$")
        if not pn then pn, pv = p, nil end
        if pn == dn then return true, pv end
    end
    return false, nil
end

-- constraint_ok: Arch dep specs ALWAYS reference pkgver, never pkgrel
-- (real: util-linux depends on "util-linux-libs=2.42.4" while the package
-- is 2.42.4-1; alpm dep matching compares pkgver-vs-pkgver). Strip the
-- pkgrel from the candidate side before comparing.
local function pkgver_only(v)
    return (v:gsub("%-[^-]*$", "", 1))
end

local function constraint_ok(ver, op, want)
    if not op then return true end
    if not ver or not want then return false end
    local c = M.vercmp(pkgver_only(ver), pkgver_only(want))
    if op == "=" then return c == 0 end
    if op == ">=" then return c >= 0 end
    if op == "<=" then return c <= 0 end
    if op == ">" then return c > 0 end
    if op == "<" then return c < 0 end
    return false
end

-- closure(requests, index): lock every package the requests need. Exact
-- name first, else PROVIDES candidates (sorted name,version; first that
-- satisfies the constraint wins); so:-deps match providers only. Cycles
-- terminate on the locked set. Unsatisfied dep = hard error (fail closed).
-- ponytail: provider scan is O(index) per dep; fine for one repo set, index
-- an alias table if DB scans ever get slow.
function M.closure(requests, index)
    local locked, queue = {}, {}
    local function add(rec)
        if not locked[rec.name] then
            locked[rec.name] = { version = rec.version, checksum = rec.checksum,
                repo = rec.repo, arch = rec.arch }
            queue[#queue + 1] = rec.name
        end
    end
    for _, n in ipairs(requests) do
        local rec = index[n]
        if not rec then error("package " .. n .. " not found in indexes", 0) end
        add(rec)
    end
    local qi = 1
    while qi <= #queue do
        local rec = index[queue[qi]] qi = qi + 1
        for _, dep in ipairs(rec.depends) do
            local dn, op, want = dep_split(dep)
            local own = (dn:sub(1, 3) ~= "so:") and index[dn] or nil
            if own and constraint_ok(own.version, op, want) then
                add(own)
            else
                local cands = {}
                for _, r in pairs(index) do
                    local found, pv = provide_version(r, dn)
                    if found and constraint_ok(pv, op, want) then
                        cands[#cands + 1] = r
                    end
                end
                table.sort(cands, function(x, y)
                    if x.name ~= y.name then return x.name < y.name end
                    return M.vercmp(x.version, y.version) < 0
                end)
                if cands[1] then add(cands[1])
                else error("unsatisfied dependency " .. dep .. " of " .. rec.name, 0) end
            end
        end
    end
    return locked
end

return M
