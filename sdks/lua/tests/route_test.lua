-- Route table tests (same semantics table as sdks/node/route.js).
-- Run: lua tests/route_test.lua (dofile skips the script main via its guard)
local here = (arg and arg[0] and arg[0]:match("^(.*)/")) or "."
dofile(here .. "/../etunnel.lua")

local fails = 0
local function ck(name, got, want)
  if tostring(got) ~= tostring(want) then
    fails = fails + 1
    print("FAIL " .. name .. ": got " .. tostring(got) .. " want " .. tostring(want))
  end
end

local rs = etunnel_normalize_routes({
  { prefix = "/", addr = "fe:1" },
  { prefix = "/api", addr = "be:1" },
  { prefix = "/api/v2", addr = "be2:1" },
})
local a = etunnel_route_for(rs, "/apiv2"); ck("/apiv2 -> /", a, "fe:1")
a = etunnel_route_for(rs, "/apiary"); ck("/apiary -> /", a, "fe:1")
a = etunnel_route_for(rs, "/api"); ck("/api exact", a, "be:1")
a = etunnel_route_for(rs, "/api/u"); ck("/api/u", a, "be:1")
a = etunnel_route_for(rs, "/api/v2/x?q"); ck("longest wins", a, "be2:1")

local ss = etunnel_normalize_routes({
  { prefix = "/api", addr = "be:1", strip = true },
  { prefix = "/", addr = "fe:1", strip = true },
})
local _, p = etunnel_route_for(ss, "/api/u?x=1&y=2"); ck("strip keeps query", p, "/u?x=1&y=2")
_, p = etunnel_route_for(ss, "/api"); ck("exact strip -> /", p, "/")
_, p = etunnel_route_for(ss, "/app?c=1"); ck("strip root prefix = original", p, "/app?c=1")

local one = etunnel_normalize_routes({ { prefix = "/api", addr = "be:1" } })
ck("no match -> nil", etunnel_route_for(one, "/other"), nil)

local ok = pcall(etunnel_normalize_routes, { { prefix = "/", addr = "" } })
ck("addr required rejected", ok, false)
ok = pcall(etunnel_normalize_routes, { { prefix = "api", addr = "a:1" } })
ck("bare prefix rejected", ok, false)
ok = pcall(etunnel_normalize_routes, { { prefix = "/api", addr = "a:1" }, { prefix = "/api/", addr = "a:2" } })
ck("duplicate rejected", ok, false)

if fails == 0 then print("lua route tests: PASS") os.exit(0) end
print("lua route tests: FAIL (" .. fails .. ")")
os.exit(1)
