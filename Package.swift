// swift-tools-version:5.9
// Root manifest: what the Swift Package Index and SPM consumers resolve -
// `.package(url: "https://github.com/topcheer/etunnel.git", from: ...)` lands
// here, not on sdks/swift/Package.swift. It therefore has to offer the same
// products as the nested manifest: the library "etunnel" (what `import etunnel`
// needs) and the "etunnel-cli" executable, with the sources relocated under
// sdks/swift. Keep the two manifests in lockstep.
//
// History (#161 fixed only the nested manifest): an earlier root manifest made
// one executable target out of the whole sdks/swift tree with no library
// product, so consumers got
//   sdks/swift/etunnel-cli/main.swift:40:1: error: statements are not allowed at the top level
// (etunnel-cli/ and Tests/ were compiled into the target) and `import etunnel`
// did not exist.
//
// The SDK's WebSocket client uses URLSessionWebSocketTask, which the Linux
// swift-corelibs-foundation has never implemented (#90): Apple-platforms-only
// by design (see sdks/swift/Package.swift).
import PackageDescription

let package = Package(
    name: "etunnel",
    platforms: [
        .macOS(.v13), .iOS(.v16)
    ],
    products: [
        .library(name: "etunnel", targets: ["etunnel"]),
        .executable(name: "etunnel-cli", targets: ["etunnel-cli"])
    ],
    targets: [
        .target(
            name: "etunnel",
            path: "sdks/swift",
            exclude: [
                "Package.swift", "README.md",
                "example", "tests", "Tests",
                "etunnel-cli"
            ]
        ),
        .executableTarget(
            name: "etunnel-cli",
            dependencies: ["etunnel"],
            path: "sdks/swift/etunnel-cli"
        ),
        .testTarget(
            name: "EtunnelTests",
            dependencies: ["etunnel"],
            path: "sdks/swift/Tests/EtunnelTests"
        )
    ]
)
