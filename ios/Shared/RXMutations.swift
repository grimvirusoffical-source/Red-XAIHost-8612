import Foundation

public enum RXValuePath: Sendable { case index(Int), key(String) }

public enum RXMutations {
    public static func setValue(_ value: RXValue, packerID: String, snapshot: RXLanguageSnapshot, currentSource: String) throws -> String {
        guard let packer = snapshot.packers.first(where: { $0.id == packerID }) else { throw RXLanguageError.missingElement }
        return try replace(span: packer.valueSpan, with: RXSerializer.value(value), snapshot: snapshot, currentSource: currentSource)
    }
    public static func remove(_ id: String, snapshot: RXLanguageSnapshot, currentSource: String) throws -> String {
        if let packer = snapshot.packers.first(where: { $0.id == id }) {
            return try replace(span: packer.span, with: "", snapshot: snapshot, currentSource: currentSource)
        }
        guard let box = snapshot.boxes.first(where: { $0.id == id }), box.name != "Red-XAI" else { throw RXLanguageError.missingElement }
        return try replace(span: box.span, with: "", snapshot: snapshot, currentSource: currentSource)
    }
    public static func addPacker(name: String, localID: Int?, globalID: Int?, value: RXValue, boxID: String,
                                 snapshot: RXLanguageSnapshot, currentSource: String) throws -> String {
        let code = try RXSerializer.packer(name: name, localID: localID, globalID: globalID, value: value)
        return try insert(code, boxID: boxID, snapshot: snapshot, currentSource: currentSource)
    }
    public static func addBox(name: String, localID: Int?, globalID: Int?, boxID: String,
                              snapshot: RXLanguageSnapshot, currentSource: String) throws -> String {
        guard name != "Red-XAI" else { throw RXLanguageError.invalid("There can be only one Red-XAI root.") }
        for id in [localID, globalID].compactMap({ $0 }) { if id < 1 { throw RXLanguageError.invalid("IDs must be positive.") } }
        let code = "{" + (try RXSerializer.name(name)) + "}" + (localID.map { "[\($0)]" } ?? "") + "{\n<[False," + (globalID.map(String.init) ?? "False") + ",false]>}"
        return try insert(code, boxID: boxID, snapshot: snapshot, currentSource: currentSource)
    }
    public static func addAccess(name: String, tokenID: Int, boxID: String, snapshot: RXLanguageSnapshot, currentSource: String) throws -> String {
        guard tokenID > 0 else { throw RXLanguageError.invalid("Access declaration IDs must be positive.") }
        let code = "<[GAccsess," + (try RXSerializer.name(name)) + ",\(tokenID),]>"
        return try insert(code, boxID: boxID, snapshot: snapshot, currentSource: currentSource)
    }
    public static func setBoxAccess(boxID: String, crossDatabase: Bool, globalID: Int?, intraFile: Bool, keyReference: String?, snapshot: RXLanguageSnapshot, currentSource: String) throws -> String {
        guard let box = snapshot.boxes.first(where: { $0.id == boxID }) else { throw RXLanguageError.missingElement }
        if let globalID, globalID < 1 { throw RXLanguageError.invalid("IDs must be positive.") }
        let root = box.name == "Red-XAI"
        if root && globalID != 1 { throw RXLanguageError.invalid("The root global ID must be 1.") }
        if root && keyReference?.hasPrefix("keyref:") != true { throw RXLanguageError.invalid("Use a public keyref: identifier, not an API secret.") }
        let extra = root ? "," + (try RXSerializer.quoted(keyReference ?? "keyref:local")) : ""
        let code = "<[" + (crossDatabase ? "True" : "False") + "," + (globalID.map(String.init) ?? "False") + "," + (intraFile ? "true" : "false") + extra + "]>"
        let target = box.access?.span ?? RXSourceSpan(offset: box.insertionOffset)
        return try replace(span: target, with: code, snapshot: snapshot, currentSource: currentSource)
    }
    private static func insert(_ code: String, boxID: String, snapshot: RXLanguageSnapshot, currentSource: String) throws -> String {
        guard let box = snapshot.boxes.first(where: { $0.id == boxID }) else { throw RXLanguageError.missingElement }
        let indent = String(repeating: "    ", count: box.depth + 1)
        let formatted = "\n" + code.components(separatedBy: "\n").map { indent + $0 }.joined(separator: "\n") + "\n" + String(repeating: "    ", count: box.depth)
        return try replace(span: .init(offset: box.insertionOffset), with: formatted, snapshot: snapshot, currentSource: currentSource)
    }
    private static func replace(span: RXSourceSpan, with replacement: String, snapshot: RXLanguageSnapshot, currentSource: String) throws -> String {
        guard snapshot.source == currentSource else { throw RXLanguageError.staleSnapshot }
        guard snapshot.isValid else { throw RXLanguageError.invalid("Resolve document errors before using structured edits.") }
        guard let range = Range(span.range, in: currentSource) else { throw RXLanguageError.missingElement }
        var next = currentSource; next.replaceSubrange(range, with: replacement)
        guard next.utf8.count <= RedXAIValidator.maxBytes else { throw RXLanguageError.limit }
        let check = RedXAILanguage.inspect(next)
        guard check.isValid else { throw RXLanguageError.invalid(check.diagnostics.first(where: { $0.severity == .error })?.message ?? "Edit would make this document invalid.") }
        return next
    }
    public static func replacing(_ original: RXValue, at path: [RXValuePath], with next: RXValue) throws -> RXValue {
        guard path.count <= RedXAILanguage.maximumDepth else { throw RXLanguageError.limit }
        guard let first = path.first else { return next }
        switch (original, first) {
        case (.array(var values), .index(let index)):
            guard values.indices.contains(index) else { throw RXLanguageError.missingElement }
            values[index] = try replacing(values[index], at: Array(path.dropFirst()), with: next); return .array(values)
        case (.table(var entries), .key(let key)), (.metatable(var entries), .key(let key)):
            guard let index = entries.firstIndex(where: { $0.key == key }) else { throw RXLanguageError.missingElement }
            entries[index].value = try replacing(entries[index].value, at: Array(path.dropFirst()), with: next)
            if case .metatable = original { return .metatable(entries) }; return .table(entries)
        default: throw RXLanguageError.invalid("The path does not match this value's structure.")
        }
    }
}
