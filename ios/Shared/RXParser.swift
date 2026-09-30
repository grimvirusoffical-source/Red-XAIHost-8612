import Foundation

struct RXParser {
    let source: String
    let allTokens: [RXToken]
    let tokens: [RXToken]
    let strict: Bool
    var diagnostics: [RXDiagnostic]
    var index = 0
    var nodeCount = 0
    struct Stop: Error {}

    init(source: String, tokens: [RXToken], diagnostics: [RXDiagnostic], strict: Bool) {
        self.source = source; allTokens = tokens; self.tokens = tokens.filter { $0.kind != .comment }
        self.diagnostics = diagnostics; self.strict = strict
    }
    var current: RXToken { tokens[min(index, tokens.count - 1)] }
    func peek(_ distance: Int = 1) -> RXToken { tokens[min(index + distance, tokens.count - 1)] }
    var done: Bool { current.kind == .eof || diagnostics.count >= RedXAILanguage.maximumDiagnostics }
    @discardableResult mutating func advance() -> RXToken { let t = current; if index < tokens.count - 1 { index += 1 }; return t }
    @discardableResult mutating func match(_ text: String) -> Bool { if current.text == text { advance(); return true }; return false }
    mutating func issue(_ message: String, _ token: RXToken? = nil, severity: RXDiagnostic.Severity = .error) {
        let at = token ?? current
        if diagnostics.count < RedXAILanguage.maximumDiagnostics {
            diagnostics.append(.init(line: at.span.line, message: message, severity: severity, column: at.span.column, offset: at.span.offset))
        }
    }
    @discardableResult mutating func expect(_ text: String, _ message: String? = nil) throws -> RXToken {
        guard current.text == text else { issue(message ?? "Expected '\(text)'."); throw Stop() }
        return advance()
    }
    mutating func name() throws -> (String, RXToken) {
        let t = current
        guard t.kind == .name || (t.kind == .string && t.decoded != nil) else { issue("Expected a name or a quoted name."); throw Stop() }
        advance()
        let value = t.decoded ?? t.text
        guard !value.isEmpty, value.count <= 128, !value.unicodeScalars.contains(where: { CharacterSet.controlCharacters.contains($0) }) else {
            issue("Names must have 1–128 characters without control characters.", t); throw Stop()
        }
        return (value, t)
    }
    mutating func positiveID() throws -> Int {
        let t = current
        guard t.kind == .number, t.text.allSatisfy({ $0.isASCII && $0.isNumber }), let id = Int(t.text), id > 0 else {
            issue("IDs must be positive whole numbers within the 64-bit integer range."); throw Stop()
        }
        advance(); return id
    }
    mutating func optionalID() throws -> Int? {
        guard match("[") else { return nil }
        if match("]") { return nil }
        if current.kind == .boolean && current.text.lowercased() == "false" { advance(); try expect("]"); return nil }
        let id = try positiveID(); try expect("]"); return id
    }
    mutating func bool() throws -> Bool {
        guard current.kind == .boolean else { issue("Expected True or False."); throw Stop() }
        return advance().text.lowercased() == "true"
    }

    mutating func parse() -> RXLanguageSnapshot {
        guard !tokens.isEmpty else { return .init(source: source, roots: [], packers: [], boxes: [], diagnostics: diagnostics, tokens: allTokens) }
        var roots: [RXBox] = []
        while !done {
            let before = index
            do {
                if current.text == "}" { issue("Unexpected closing brace without an opening box."); advance(); continue }
                if current.text == "]" { issue("Unexpected closing bracket without an opening bracket."); advance(); continue }
                let element = try element(parent: "", path: "", depth: 0)
                if case .box(let box) = element { roots.append(box) }
                else { issue("Only Boxes can appear at document level.") }
            } catch { recover(after: before) }
        }
        let main = roots.filter { $0.name == "Red-XAI" }
        if main.count != 1 || roots.first?.name != "Red-XAI" || main.first?.localID != 1 {
            issue("Missing required Red-XAI root: the first Box must be {Red-XAI}[1], exactly once.", tokens[0])
        }
        var boxes: [RXBox] = [], packers: [RXPacker] = []
        func collect(_ box: RXBox) {
            boxes.append(box)
            for item in box.elements {
                switch item { case .box(let b): collect(b); case .packer(let p): packers.append(p); case .access: break }
            }
        }
        roots.forEach(collect)
        validateIDs(boxes: boxes, packers: packers)
        diagnostics.sort { ($0.offset, $0.line, $0.column, $0.message) < ($1.offset, $1.line, $1.column, $1.message) }
        return .init(source: source, roots: roots, packers: packers, boxes: boxes, diagnostics: diagnostics, tokens: allTokens)
    }
    mutating func recover(after previous: Int) {
        if index <= previous && current.kind != .eof { advance() }
        while !done && !["{", "}", "<"].contains(current.text) { advance() }
    }
    mutating func element(parent: String, path: String, depth: Int) throws -> RXElement {
        nodeCount += 1
        guard nodeCount <= RedXAILanguage.maximumNodes, depth <= RedXAILanguage.maximumDepth else {
            issue("Node or nesting-depth limit exceeded."); index = tokens.count - 1; throw Stop()
        }
        let start = try expect("{", "Expected a Box or Packer starting with '{'.")
        let (objectName, _) = try name()
        try expect("}")
        let localID = try optionalID()
        let objectPath = path + "/" + objectName
        if match("=") {
            let opening = try expect("[")
            let rawStart = current.span
            let value = try collection(depth: depth + 1, forceArray: false)
            let closeValue = try expect("]")
            let globalID = try optionalID()
            let end = try expect(",", "Packer assignments must end with a comma.")
            let valueSpan = RXSourceSpan(offset: opening.span.end, length: closeValue.span.offset - opening.span.end,
                                        line: rawStart.line, column: rawStart.column)
            return .packer(.init(name: objectName, localID: localID, value: value, globalID: globalID,
                                 parentID: parent, path: objectPath, span: span(start, through: end), valueSpan: valueSpan))
        }
        try expect("{", "A Box needs an opening '{'; a Packer needs '='.")
        let boxID = "box:\(start.span.offset)"
        var items: [RXElement] = []
        var access: RXBoxAccess?
        var insertion = current.span.offset
        while !done && current.text != "}" {
            let before = index
            do {
                if current.text == "<" {
                    if peek().text == "[" && peek(2).text == "GAccsess" {
                        if access != nil { issue("Access declarations must precede the Box closing metadata.") }
                        items.append(.access(try declaration()))
                    } else {
                        if access != nil { issue("A Box can have only one closing metadata declaration.") }
                        insertion = current.span.offset
                        access = try footer(root: objectName == "Red-XAI")
                        if current.text != "}" { issue("Close the Box with '}' immediately after its metadata.") }
                    }
                } else if current.text == "]" {
                    issue("Unexpected closing bracket inside a Box."); advance()
                } else {
                    if access != nil { issue("Box contents must precede closing metadata.") }
                    items.append(try element(parent: boxID, path: objectPath, depth: depth + 1))
                }
            } catch { recover(after: before) }
            if index == before && !done { advance() }
        }
        if access == nil {
            insertion = current.span.offset
            issue("Box '\(objectName)' has no closing access metadata; no remote access is implied.", start,
                  severity: strict ? .error : .warning)
        }
        let end = try expect("}", "Unclosed Box; expected a closing brace '}'.")
        return .box(.init(name: objectName, localID: localID, access: access, elements: items, path: objectPath,
                          depth: depth, span: span(start, through: end), insertionOffset: insertion))
    }
    mutating func footer(root: Bool) throws -> RXBoxAccess {
        let start = try expect("<"); try expect("[")
        let cross = try bool(); try expect(",")
        let globalID: Int?
        if current.kind == .boolean && current.text.lowercased() == "false" { advance(); globalID = nil }
        else { globalID = try positiveID() }
        try expect(",")
        let intra = try bool()
        var key: String?
        if match(",") {
            let (value, token) = try name(); key = value
            if !value.hasPrefix("keyref:") { issue("Use a public keyref: identifier here, never a reusable API secret.", token, severity: .warning) }
        }
        try expect("]"); let end = try expect(">")
        if root && (key == nil || globalID != 1) {
            issue("The Red-XAI root closing metadata needs global ID 1 and a public key reference.", start,
                  severity: strict ? .error : .warning)
        }
        if !root && key != nil { issue("Only the Red-XAI root has a key-reference field.", start) }
        return .init(crossDatabase: cross, globalID: globalID, intraFile: intra, keyReference: key, span: span(start, through: end))
    }
    mutating func declaration() throws -> RXAccessDeclaration {
        let start = try expect("<"); try expect("["); try expect("GAccsess"); try expect(",")
        let (value, _) = try name(); try expect(","); let id = try positiveID()
        _ = match(","); try expect("]"); let end = try expect(">")
        return .init(name: value, tokenID: id, span: span(start, through: end))
    }
    mutating func collection(depth: Int, forceArray: Bool) throws -> RXValue {
        guard depth <= RedXAILanguage.maximumDepth else { issue("Value nesting-depth limit exceeded."); throw Stop() }
        if current.text == "]" { return .array([]) }
        let table = (current.kind == .name || current.kind == .string) && peek().text == "="
        if table {
            var fields: [RXTableEntry] = [], keys = Set<String>()
            while !done && current.text != "]" {
                let (key, at) = try name(); try expect("=")
                if !keys.insert(key).inserted { issue("Duplicate table key '\(key)'.", at) }
                fields.append(.init(key: key, value: try atom(depth: depth + 1)))
                if !match(",") { break }
            }
            return .table(fields)
        }
        var values = [try atom(depth: depth + 1)], hadComma = false
        while match(",") {
            hadComma = true
            if current.text == "]" { break }
            values.append(try atom(depth: depth + 1))
        }
        return forceArray || hadComma ? .array(values) : values[0]
    }
    mutating func atom(depth: Int) throws -> RXValue {
        guard depth <= RedXAILanguage.maximumDepth else { issue("Value nesting-depth limit exceeded."); throw Stop() }
        let token = current
        switch token.kind {
        case .number:
            guard let value = Double(token.text), value.isFinite else { issue("Non-finite numbers are not supported."); throw Stop() }
            advance(); return .number(value)
        case .string:
            guard let value = token.decoded else { throw Stop() }
            advance(); return .string(value)
        case .boolean: advance(); return .boolean(token.text.lowercased() == "true")
        case .nell: advance(); return .nell
        default: break
        }
        if match("[") { let value = try collection(depth: depth + 1, forceArray: true); try expect("]"); return value }
        if current.text == "meta" || current.text == "table" {
            let type = advance().text
            try expect("[")
            if match("]") { return type == "meta" ? .metatable([]) : .table([]) }
            let value = try collection(depth: depth + 1, forceArray: true); try expect("]")
            guard case .table(let entries) = value else { issue("\(type) requires named key = value entries.", token); throw Stop() }
            return type == "meta" ? .metatable(entries) : .table(entries)
        }
        issue("Expected a number, string, Boolean, NELL, array, table, or meta table."); throw Stop()
    }
    func span(_ start: RXToken, through end: RXToken) -> RXSourceSpan {
        .init(offset: start.span.offset, length: max(0, end.span.end - start.span.offset), line: start.span.line, column: start.span.column)
    }
    mutating func validateIDs(boxes: [RXBox], packers: [RXPacker]) {
        var localBoxes: [Int: RXBox] = [:], globalBoxes: [Int: RXBox] = [:]
        var localPackers: [String: RXPacker] = [:], globalPackers: [String: RXPacker] = [:]
        func token(_ span: RXSourceSpan) -> RXToken { .init(kind: .name, text: "", decoded: nil, span: span) }
        for box in boxes {
            if box.name == "Red-XAI" && box.depth != 0 { issue("Red-XAI is reserved for the single document root.", token(box.span)) }
            if let id = box.localID {
                if let first = localBoxes[id] { issue("Duplicate local Box ID \(id); first used at line \(first.span.line).", token(box.span)) }
                else { localBoxes[id] = box }
            }
            if let id = box.globalID {
                if let first = globalBoxes[id] { issue("Duplicate global Box ID \(id); first used at line \(first.span.line).", token(box.span)) }
                else { globalBoxes[id] = box }
            }
            var grants = Set<String>()
            for case .access(let grant) in box.elements {
                if !grants.insert("\(grant.name)#\(grant.tokenID)").inserted { issue("Duplicate GAccsess name and ID in this Box.", token(grant.span)) }
            }
        }
        for packer in packers {
            if let id = packer.localID {
                let key = "\(packer.parentID)|\(packer.name)#\(id)"
                if let first = localPackers[key] { issue("Duplicate local Packer name/ID; first used at line \(first.line) in this Box.", token(packer.span)) }
                else { localPackers[key] = packer }
            }
            if let id = packer.globalID {
                let key = "\(packer.name)#\(id)"
                if let first = globalPackers[key] { issue("Duplicate global Packer name/ID; first used at line \(first.line).", token(packer.span)) }
                else { globalPackers[key] = packer }
            }
        }
    }
}
