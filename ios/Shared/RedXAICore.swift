import Foundation
#if canImport(SwiftUI)
import SwiftUI
#endif

public struct RXDiagnostic: Identifiable, Equatable, Sendable {
    public var line: Int
    public var column: Int
    public var offset: Int
    public var message: String
    public var severity: Severity
    public var id: String { "\(offset):\(line):\(column):\(severity.rawValue):\(message)" }
    public enum Severity: String, Sendable { case error, warning }
    public init(line: Int, message: String, severity: Severity, column: Int = 1, offset: Int = 0) {
        self.line = line; self.column = column; self.offset = offset
        self.message = message; self.severity = severity
    }
}

public struct RXValidationResult: Sendable {
    public var diagnostics: [RXDiagnostic]
    public var isValid: Bool { !diagnostics.contains { $0.severity == .error } }
    public init(_ diagnostics: [RXDiagnostic]) { self.diagnostics = diagnostics }
}

public enum RedXAIValidator {
    public static let maxBytes = 2 * 1024 * 1024
    public static func validate(_ source: String) -> RXValidationResult {
        .init(RedXAILanguage.inspect(source).diagnostics)
    }
}

#if canImport(SwiftUI)
public enum RedXAITheme {
    public static let blood = Color(red: 0.62, green: 0.05, blue: 0.12)
    public static let purple = Color(red: 0.39, green: 0.20, blue: 0.55)
    public static let panel = Color(red: 0.075, green: 0.045, blue: 0.075)
}
#endif
