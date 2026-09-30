import Foundation

public struct RXToken: Equatable, Sendable {
    public enum Kind: String, Sendable { case name, number, string, boolean, nell, symbol, comment, eof }
    public var kind: Kind
    public var text: String
    public var decoded: String?
    public var span: RXSourceSpan
}

public struct RXLexResult: Sendable {
    public var tokens: [RXToken]
    public var diagnostics: [RXDiagnostic]
}

/// Bounded, Unicode-aware scanner. Ranges use UTF-16 for TextKit interoperability.
public enum RXLexer {
    public static func scan(_ source: String) -> RXLexResult {
        guard source.utf8.count <= RedXAIValidator.maxBytes else {
            return .init(tokens: [], diagnostics: [.init(line: 1, message: "Document exceeds 2 MiB.", severity: .error)])
        }
        var worker = Worker(source)
        return worker.run()
    }
    private struct Worker {
        let chars: [Unicode.Scalar]
        var position = 0, offset = 0, line = 1, column = 1
        var previousCR = false
        var tokens: [RXToken] = []
        var diagnostics: [RXDiagnostic] = []
        init(_ source: String) { chars = Array(source.unicodeScalars) }
        var current: Unicode.Scalar? { position < chars.count ? chars[position] : nil }
        func peek(_ distance: Int = 1) -> Unicode.Scalar? { position + distance < chars.count ? chars[position + distance] : nil }
        mutating func advance() {
            guard let c = current else { return }
            position += 1; offset += c.value > 0xFFFF ? 2 : 1
            if c == "\r" { line += 1; column = 1; previousCR = true }
            else if c == "\n" { if !previousCR { line += 1 }; column = 1; previousCR = false }
            else { column += 1; previousCR = false }
        }
        func text(_ begin: Int) -> String { String(String.UnicodeScalarView(chars[begin..<position])) }
        func isDigit(_ c: Unicode.Scalar?) -> Bool { guard let c else { return false }; return (48...57).contains(c.value) }
        func isName(_ c: Unicode.Scalar) -> Bool {
            CharacterSet.alphanumerics.contains(c) || "_-.:".unicodeScalars.contains(c)
        }
        mutating func issue(_ message: String, at span: RXSourceSpan) {
            if diagnostics.count < RedXAILanguage.maximumDiagnostics {
                diagnostics.append(.init(line: span.line, message: message, severity: .error, column: span.column, offset: span.offset))
            }
        }
        mutating func run() -> RXLexResult {
            while let c = current {
                if tokens.count >= RedXAILanguage.maximumTokens {
                    issue("Token limit exceeded.", at: .init(offset: offset, line: line, column: column)); break
                }
                if CharacterSet.whitespacesAndNewlines.contains(c) || (position == 0 && c.value == 0xFEFF) { advance(); continue }
                let begin = position
                var span = RXSourceSpan(offset: offset, line: line, column: column)
                var kind = RXToken.Kind.symbol
                var decoded: String?
                if c == "/" && peek() == "-" {
                    advance(); advance()
                    // A bare /- line unambiguously opens a multiline comment.
                    var probe = position
                    while probe < chars.count && (chars[probe] == " " || chars[probe] == "\t") { probe += 1 }
                    let multiline = probe == chars.count || chars[probe] == "\n" || chars[probe] == "\r"
                    var closed = false
                    while let q = current {
                        if q == "-", peek() == "\\" || peek() == "/" {
                            advance(); advance(); closed = true; break
                        }
                        if !multiline && (q == "\r" || q == "\n") { break }
                        advance()
                    }
                    if multiline && !closed { issue("Unterminated multiline comment; expected -\\.", at: span) }
                    kind = .comment
                } else if c == "\"" {
                    advance()
                    var escaped = false, closed = false
                    while let q = current {
                        if q == "\r" || q == "\n" { break }
                        advance()
                        if escaped { escaped = false; continue }
                        if q == "\\" { escaped = true; continue }
                        if q == "\"" { closed = true; break }
                    }
                    kind = .string
                    if !closed { issue("Unterminated string.", at: span) }
                    else {
                        do { decoded = try JSONDecoder().decode(String.self, from: Data(text(begin).utf8)) }
                        catch { issue("Invalid string escape or control character.", at: span) }
                    }
                } else if isDigit(c) || (c == "-" && isDigit(peek())) {
                    advance()
                    while let q = current, isName(q) || q == "+" { advance() }
                    let value = text(begin)
                    let pattern = #"^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$"#
                    if value.range(of: pattern, options: .regularExpression) == nil || Double(value)?.isFinite != true {
                        issue("Invalid or non-finite number.", at: span)
                    }
                    kind = .number
                } else if isName(c) {
                    advance()
                    while let q = current, isName(q) { advance() }
                    switch text(begin).lowercased() {
                    case "true", "false": kind = .boolean
                    case "nell": kind = .nell
                    default: kind = .name
                    }
                } else {
                    advance()
                    if !"{}[]=,<>".unicodeScalars.contains(c) { issue("Unexpected character '\(c)'.", at: span) }
                }
                span.length = offset - span.offset
                tokens.append(.init(kind: kind, text: text(begin), decoded: decoded, span: span))
            }
            tokens.append(.init(kind: .eof, text: "", decoded: nil, span: .init(offset: offset, line: line, column: column)))
            return .init(tokens: tokens, diagnostics: diagnostics)
        }
    }
}
