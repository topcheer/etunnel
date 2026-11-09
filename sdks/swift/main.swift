// etunnel CLI entry point. Run: swift run etunnel 3000
//
// The client itself lives in etunnel.swift (a single file with no other
// dependencies); this file only wires it to the command line.

import Foundation

func log(_ s: String) { FileHandle.standardError.write((s + "\n").data(using: .utf8)!) }

let server = ProcessInfo.processInfo.environment["ETUNNEL_SERVER"] ?? "wss://t.etun.uk/tunnel"
let local = CommandLine.arguments.count > 1 ? CommandLine.arguments[1]
    : ProcessInfo.processInfo.environment["ETUNNEL_LOCAL"] ?? "3000"
let et = Etunnel(server: server, local: local)
do {
    let url = try await et.connect(
        token: ProcessInfo.processInfo.environment["ETUNNEL_TOKEN"],
        subdomain: ProcessInfo.processInfo.environment["ETUNNEL_SUBDOMAIN"])
    log("TUNNEL_URL: \(url)")
    while true { try await Task.sleep(nanoseconds: 1_000_000_000) }
} catch {
    log("etunnel: \(error)")
    exit(1)
}
