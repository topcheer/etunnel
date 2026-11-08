package = "ggcode.etunnel"
version = "1.0.0-1"
source = {
  url = "git://github.com/ggcode/etunnel",
  tag = "v1.0.0"
}
description = "Expose a local service through an etunnel reverse-tunnel server"
license = "MIT"
dependencies = {
  "lua >= 5.2",
  "luasocket >= 3.0",
  "luasec >= 1.0"
}
build = {
  type = "none",
  install = {
    lua = { ["ggcode.etunnel"] = "etunnel.lua" }
  }
}
