import Foundation
import SwiftUI

public struct RXDiagnostic: Identifiable, Equatable, Sendable {
    public let id = UUID()
    public var line: Int
    public var message: String
    public var severity: Severity
    public enum Severity: String, Sendable { case error, warning }
    public init(line: Int, message: String, severity: Severity) { self.line=line; self.message=message; self.severity=severity }
}

public struct RXValidationResult: Sendable {
    public var diagnostics: [RXDiagnostic]
    public var isValid: Bool { !diagnostics.contains { $0.severity == .error } }
    public init(_ diagnostics: [RXDiagnostic]) { self.diagnostics=diagnostics }
}

public enum RedXAIValidator {
    public static let maxBytes = 2 * 1024 * 1024
    public static func validate(_ source: String) -> RXValidationResult {
        if source.utf8.count > maxBytes { return .init([.init(line:1,message:"Document exceeds 2 MiB.",severity:.error)]) }
        let lines=source.split(separator:"\n",omittingEmptySubsequences:false)
        guard lines.contains(where:{String($0).contains("{Red-XAI}[1]{")}) else { return .init([.init(line:1,message:"Missing required Red-XAI root.",severity:.error)]) }
        var d:[RXDiagnostic]=[]; var curly=0; var square=0; var quoted=false; var escaped=false
        for (i,raw) in lines.enumerated() {
            let line=String(raw)
            for ch in line {
                if escaped { escaped=false; continue }
                if ch=="\\" && quoted { escaped=true; continue }
                if ch=="\"" { quoted.toggle(); continue }
                if quoted { continue }
                if ch=="{" {curly+=1}; if ch=="}" {curly-=1}; if ch=="[" {square+=1}; if ch=="]" {square-=1}
            }
            let t=line.trimmingCharacters(in:.whitespaces)
            if t.contains("=") && t.contains("{") && !t.hasSuffix(",") { d.append(.init(line:i+1,message:"Packer assignments must end with a comma.",severity:.error)) }
        }
        if quoted { d.append(.init(line:lines.count,message:"Unterminated string.",severity:.error)) }
        if curly != 0 { d.append(.init(line:lines.count,message:"Unbalanced braces.",severity:.error)) }
        if square != 0 { d.append(.init(line:lines.count,message:"Unbalanced brackets.",severity:.error)) }
        return .init(d)
    }
}

public enum RedXAITheme {
    public static let blood=Color(red:0.62,green:0.05,blue:0.12)
    public static let purple=Color(red:0.39,green:0.20,blue:0.55)
    public static let panel=Color(red:0.075,green:0.045,blue:0.075)
}
