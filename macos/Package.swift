// swift-tools-version: 5.10
import PackageDescription

let package = Package(
    name: "SubBar",
    platforms: [
        .macOS(.v14)
    ],
    products: [
        .executable(name: "SubBar", targets: ["SubBar"])
    ],
    targets: [
        .executableTarget(
            name: "SubBar",
            path: "Sources/SubBar"
        )
    ]
)
