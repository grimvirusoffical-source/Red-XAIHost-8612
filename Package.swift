// swift-tools-version: 6.0
import PackageDescription
var coreSources = ["RedXAICore.swift", "RedXAILanguage.swift", "RXLexer.swift", "RXParser.swift", "RXSerializer.swift", "RXMutations.swift", "RXTheme.swift", "RXDocumentCodec.swift", "RXNodeDraft.swift"]
var excludes = ["RedXAIUI.swift", "RedXAISyntax.swift", "PrivacyInfo.xcprivacy"]
var testExcludes: [String] = []
#if os(Windows)
// Parser, mutations and themes are portable. POSIX file locking is Apple/Linux-only for this release.
excludes.append("RXLibrary.swift")
testExcludes.append("RXLibraryTests.swift")
#else
coreSources.append("RXLibrary.swift")
#endif
let package = Package(
    name: "RedXAIDatabaseCore",
    products: [.library(name: "RedXAICore", targets: ["RedXAICore"])],
    targets: [
        .target(name: "RedXAICore", path: "ios/Shared", exclude: excludes, sources: coreSources),
        .testTarget(name: "RedXAICoreTests", dependencies: ["RedXAICore"], path: "ios/Tests", exclude: testExcludes)
    ]
)
