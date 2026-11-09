// swift-tools-version:5.9
import PackageDescription

let package = Package(
    name: "etunnel",
    products: [
        .executable(name: "etunnel", targets: ["etunnel"])
    ],
    targets: [
        .executableTarget(
            name: "etunnel",
            path: ".",
            exclude: ["example", "README.md"]
        )
    ]
)
