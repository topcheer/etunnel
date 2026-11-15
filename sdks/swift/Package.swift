// swift-tools-version:5.9
import PackageDescription

// The SDK's WebSocket client uses URLSessionWebSocketTask, which the Linux
// swift-corelibs-foundation has never implemented (#90): this package is
// Apple-platforms-only by design. Compile verification runs on a macOS CI
// job; Linux runners must not attempt swift build.
//
// Structure (#161, Swift Package Index build failures): the library
// product "etunnel" is what SPI consumers depend on; the CLI lives in a
// separate executable target (etunnel-cli) so its top-level main.swift
// never leaks into the library module. Tests live ONLY under Tests/.
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
            path: ".",
            exclude: [
                "Package.swift", "README.md",
                "example", "tests", "Tests",
                "etunnel-cli"
            ]
        ),
        .executableTarget(
            name: "etunnel-cli",
            dependencies: ["etunnel"],
            path: "etunnel-cli"
        ),
        .testTarget(
            name: "EtunnelTests",
            dependencies: ["etunnel"],
            path: "Tests/EtunnelTests"
        )
    ]
)
