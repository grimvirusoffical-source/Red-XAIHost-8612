import Foundation

/// Themes are color data, not stylesheets executed by a web view.
public struct RXEditorTheme: Codable, Equatable, Sendable {
    public var name: String
    public var appearance: String
    public var colors: [String: String]
    public static let keys = ["background", "foreground", "box", "packer", "square", "curly", "symbol", "number", "boolean", "string", "nell", "comment"]
    public static let dark = RXEditorTheme(name: "Blood Night", appearance: "dark", colors: [
        "background": "#100B13", "foreground": "#FFFFFF", "box": "#D98336", "packer": "#FFAE62",
        "square": "#F0D176", "curly": "#D4AC45", "symbol": "#79CF8A", "number": "#88B7FF",
        "boolean": "#62DAD3", "string": "#D599FF", "nell": "#B3F864", "comment": "#B0A9B4"
    ])
    public static let light = RXEditorTheme(name: "Paper", appearance: "light", colors: [
        "background": "#FAF8FC", "foreground": "#1D1623", "box": "#884307", "packer": "#9C460C",
        "square": "#705613", "curly": "#73540B", "symbol": "#1A6234", "number": "#244DA6",
        "boolean": "#086561", "string": "#78329D", "nell": "#3F6013", "comment": "#625C68"
    ])
    public func validate() throws {
        guard !name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty, name.count <= 40,
              ["dark", "light"].contains(appearance), Set(colors.keys) == Set(Self.keys) else {
            throw RXLanguageError.invalid("Invalid theme name, appearance, or color fields.")
        }
        for value in colors.values {
            guard value.range(of: #"^#[0-9A-Fa-f]{6}$"#, options: .regularExpression) != nil else {
                throw RXLanguageError.invalid("Theme colors must be six-digit #RRGGBB values.")
            }
        }
        let bg = colors["background"]!
        for key in Self.keys where key != "background" {
            guard Self.contrast(colors[key]!, bg) >= 4.5 else {
                throw RXLanguageError.invalid("The \(key) color needs at least 4.5:1 contrast against the editor background.")
            }
        }
    }
    public static func contrast(_ first: String, _ second: String) -> Double {
        func luminance(_ hex: String) -> Double {
            guard let n = UInt32(hex.dropFirst(), radix: 16) else { return 0 }
            let rgb = [Double((n >> 16) & 255), Double((n >> 8) & 255), Double(n & 255)].map { value -> Double in
                let v = value / 255; return v <= 0.04045 ? v / 12.92 : pow((v + 0.055) / 1.055, 2.4)
            }
            return rgb[0] * 0.2126 + rgb[1] * 0.7152 + rgb[2] * 0.0722
        }
        let a = luminance(first), b = luminance(second)
        return (max(a, b) + 0.05) / (min(a, b) + 0.05)
    }
    public func cssTemplate() -> String {
        "/* Red-XAI color theme: only --rx-* hexadecimal color variables are supported. */\n:root {\n" + Self.keys.map { "    --rx-\($0): \(colors[$0]!);" }.joined(separator: "\n") + "\n}\n"
    }
    public static func importingCSS(_ input: String, name: String, appearance: String) throws -> RXEditorTheme {
        guard input.utf8.count <= 8192 else { throw RXLanguageError.limit }
        let withoutComments = input.replacingOccurrences(of: #"/\*[\s\S]*?\*/"#, with: "", options: .regularExpression).trimmingCharacters(in: .whitespacesAndNewlines)
        guard withoutComments.hasPrefix(":root"), let open = withoutComments.firstIndex(of: "{"), withoutComments.last == "}",
              String(withoutComments[..<open]).trimmingCharacters(in: .whitespacesAndNewlines) == ":root" else {
            throw RXLanguageError.invalid("Use the exported :root color-variable template. Selectors, scripts, URLs, and imports are not supported.")
        }
        let body = withoutComments[withoutComments.index(after: open)..<withoutComments.index(before: withoutComments.endIndex)]
        var colors: [String: String] = [:]
        for item in body.split(separator: ";", omittingEmptySubsequences: true) {
            if item.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty { continue }
            let pieces = item.split(separator: ":", omittingEmptySubsequences: false)
            guard pieces.count == 2 else { throw RXLanguageError.invalid("Invalid theme declaration.") }
            let key = pieces[0].trimmingCharacters(in: .whitespacesAndNewlines)
            guard key.hasPrefix("--rx-") else { throw RXLanguageError.invalid("Only --rx- color variables are allowed.") }
            let token = String(key.dropFirst(5))
            guard Self.keys.contains(token), colors[token] == nil else { throw RXLanguageError.invalid("Unknown or duplicate theme color.") }
            colors[token] = pieces[1].trimmingCharacters(in: .whitespacesAndNewlines).uppercased()
        }
        let theme = RXEditorTheme(name: name, appearance: appearance, colors: colors)
        try theme.validate(); return theme
    }
}
