-- Minimal etunnel example (Lua). Requires lua-http (luarocks install http).
--   lua example/basic.lua 3000
package.path = "./?.lua;" .. package.path
local etunnel = require("etunnel")

local port = tonumber(arg[1]) or 3000
local t = etunnel.connect("wss://t.etun.uk/tunnel", port)
print("public URL: " .. (t.url or "?"))

-- keep the tunnel alive
while true do
  etunnel.pump and etunnel.pump(t) or os.execute("sleep 1")
end
