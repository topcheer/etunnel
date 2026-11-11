// swift-tools-version:5.9
import PackageDescription

// The SDK's WebSocket client uses URLSessionWebSocketTask, which the Linux
// swift-corelibs-foundation has never implemented (#90): this package is
// Apple-platforms-only by design. Compile verification runs on a macOS CI
// job; Linux runners must not attempt swift build.
let package = Package(
    name: "etunnel",
    platforms: [
        .macOS(.v13), .iOS(.v16)
    ],
    products: [
        .executable(name: "etunnel", targets: ["etunnel"])
    ],
    targets: [
        .executableTarget(
            name: "etunnel",
            path: ".",
            exclude: ["example", "README.md", "tests"]
        )
    ]
)
