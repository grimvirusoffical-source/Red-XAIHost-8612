import Foundation

public struct RXSourceSpan: Equatable, Codable, Sendable {
    /// UTF-16 offsets, compatible with NSRange and native text views.
    public var offset: Int
    public var length: Int
    public var line: Int
    public var column: Int
    public var range: NSRange { NSRange(location: offset, length: length) }
    public var end: Int { offset + length }
    public init(offset: Int = 0, length: Int = 0, line: Int = 1, column: Int = 1) {
        self.offset = offset; self.length = length; self.line = line; self.column = column
    }
}

public indirect enum RXValue: Equatable, Codable, Sendable {
    case number(Double)
    case string(String)
    case boolean(Bool)
    case nell
    case array([RXValue])
    case table([RXTableEntry])
    /// Metadata only. No Lua, JavaScript, or custom code is executed.
    case metatable([RXTableEntry])
    public var typeName: String {
        switch self {
        case .number: return "Number"
        case .string: return "String"
        case .boolean: return "Boolean"
        case .nell: return "NELL"
        case .array: return "Array"
        case .table: return "Table"
        case .metatable: return "Metatable"
        }
    }
}

public struct RXTableEntry: Equatable, Codable, Sendable {
    public var key: String
    public var value: RXValue
    public init(key: String, value: RXValue) { self.key = key; self.value = value }
}

public struct RXPacker: Identifiable, Equatable, Sendable {
    public var id: String { "packer:\(span.offset)" }
    public var name: String
    public var localID: Int?
    public var value: RXValue
    public var globalID: Int?
    public var parentID: String
    public var path: String
    public var span: RXSourceSpan
    public var valueSpan: RXSourceSpan
    public var line: Int { span.line }
}

public struct RXAccessDeclaration: Identifiable, Equatable, Sendable {
    public var id: String { "grant:\(span.offset)" }
    public var name: String
    public var tokenID: Int
    public var span: RXSourceSpan
}

public struct RXBoxAccess: Equatable, Sendable {
    public var crossDatabase: Bool
    public var globalID: Int?
    public var intraFile: Bool
    public var keyReference: String?
    public var span: RXSourceSpan
}

public indirect enum RXElement: Equatable, Sendable {
    case box(RXBox)
    case packer(RXPacker)
    case access(RXAccessDeclaration)
    public var span: RXSourceSpan {
        switch self { case .box(let b): return b.span; case .packer(let p): return p.span; case .access(let a): return a.span }
    }
}

public struct RXBox: Identifiable, Equatable, Sendable {
    public var id: String { "box:\(span.offset)" }
    public var name: String
    public var localID: Int?
    public var access: RXBoxAccess?
    public var elements: [RXElement]
    public var path: String
    public var depth: Int
    public var span: RXSourceSpan
    public var insertionOffset: Int
    public var globalID: Int? { access?.globalID }
}

public struct RXLanguageSnapshot: Sendable {
    public var source: String
    public var roots: [RXBox]
    public var packers: [RXPacker]
    public var boxes: [RXBox]
    public var diagnostics: [RXDiagnostic]
    public var tokens: [RXToken]
    public var isValid: Bool { !diagnostics.contains { $0.severity == .error } }
    public func find(name: String) -> [RXPacker] { packers.filter { $0.name.localizedCaseInsensitiveContains(name) } }
    public func find(localID: Int) -> [RXPacker] { packers.filter { $0.localID == localID } }
    public func find(globalID: Int) -> [RXPacker] { packers.filter { $0.globalID == globalID } }
    public func findBoxes(name: String) -> [RXBox] { boxes.filter { $0.name.localizedCaseInsensitiveContains(name) } }
}

public enum RedXAILanguage {
    public static let maximumDepth = 64
    public static let maximumTokens = 200_000
    public static let maximumNodes = 20_000
    public static let maximumDiagnostics = 100
    public static func inspect(_ source: String, strict: Bool = false) -> RXLanguageSnapshot {
        guard source.utf8.count <= RedXAIValidator.maxBytes else {
            return .init(source: "", roots: [], packers: [], boxes: [], diagnostics: [.init(line: 1, message: "Document exceeds 2 MiB.", severity: .error)], tokens: [])
        }
        let scan = RXLexer.scan(source)
        var parser = RXParser(source: source, tokens: scan.tokens, diagnostics: scan.diagnostics, strict: strict)
        return parser.parse()
    }
    public static func parseValue(_ source: String) throws -> RXValue {
        let wrapper = "{Red-XAI}[1]{\n{Value} = [\(source)],\n<[False,1,false,\"keyref:local\"]>}"
        let result = inspect(wrapper, strict: true)
        guard result.isValid, result.roots.count == 1, result.boxes.count == 1,
              result.packers.count == 1, let packer = result.packers.first,
              (result.source as NSString).substring(with: packer.valueSpan.range) == source else {
            throw RXLanguageError.invalid(result.diagnostics.first(where: { $0.severity == .error })?.message ?? "Invalid value.")
        }
        return packer.value
    }
}

public enum RXLanguageError: Error, LocalizedError, Sendable {
    case invalid(String), staleSnapshot, missingElement, limit
    public var errorDescription: String? {
        switch self {
        case .invalid(let text): return text
        case .staleSnapshot: return "The document changed. Refresh the outline before applying this edit."
        case .missingElement: return "The selected item is no longer present."
        case .limit: return "The operation exceeds a database safety limit."
        }
    }
}
