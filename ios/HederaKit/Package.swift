// swift-tools-version:6.0

import PackageDescription

let package = Package(
    name: "HederaKit",
    platforms: [.iOS(.v17), .macOS(.v14)],
    products: [
        .library(name: "HederaKit", targets: ["HederaKit"]),
    ],
    targets: [
        .target(name: "HederaKit"),
        .testTarget(name: "HederaKitTests", dependencies: ["HederaKit"]),
    ]
)
