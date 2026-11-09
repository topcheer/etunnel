// swift-tools-version:5.9
// Root manifest for the Swift Package Index and SPM consumers; sources live
// in sdks/swift (see also sdks/swift/README.md for usage).
import PackageDescription

let package = Package(
    name: "etunnel",
    products: [
        .executable(name: "etunnel", targets: ["etunnel"])
    ],
    targets: [
        .executableTarget(
            name: "etunnel",
            path: "sdks/swift",
            exclude: ["Package.swift", "README.md", "example"]
        )
    ]
)
