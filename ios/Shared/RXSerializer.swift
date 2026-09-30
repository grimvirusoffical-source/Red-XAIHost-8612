import Foundation

public enum RXSerializer {
    public static func quoted(_ text: String) throws -> String {
        String(decoding: try JSONEncoder().encode(text), as: UTF8.self)
    }
    public static func name(_ text: String) throws -> String {
        guard !text.isEmpty, text.count <= 128, !text.unicodeScalars.contains(where: { CharacterSet.controlCharacters.contains($0) }) else {
            throw RXLanguageError.invalid("Names must have 1–128 characters without control characters.")
        }
        let ordinary = text.range(of: #"^[A-Za-z_][A-Za-z0-9_.:-]*$"#, options: .regularExpression) != nil
        return ordinary && !["true", "false", "nell"].contains(text.lowercased()) ? text : try quoted(text)
    }
    public static func value(_ value: RXValue, depth: Int = 0) throws -> String {
        guard depth <= RedXAILanguage.maximumDepth else { throw RXLanguageError.limit }
        switch value {
        case .number(let number):
            guard number.isFinite else { throw RXLanguageError.invalid("Non-finite numbers cannot be serialized.") }
            if number == 0 && number.sign == .minus { return "-0.0" }
            return String(number)
        case .string(let text): return try quoted(text)
        case .boolean(let bool): return bool ? "True" : "False"
        case .nell: return "NELL"
        case .array(let values): return "[" + (try values.map { try self.value($0, depth: depth + 1) }.joined(separator: ", ")) + "]"
        case .table(let entries), .metatable(let entries):
            guard Set(entries.map(\.key)).count == entries.count else { throw RXLanguageError.invalid("Duplicate table keys.") }
            let content = try entries.map { try name($0.key) + " = " + self.value($0.value, depth: depth + 1) }.joined(separator: ", ")
            let prefix: String
            if case .metatable = value { prefix = "meta" } else { prefix = "table" }
            return prefix + "[" + content + "]"
        }
    }
    public static func packer(name: String, localID: Int?, globalID: Int?, value: RXValue) throws -> String {
        for id in [localID, globalID].compactMap({ $0 }) { if id < 1 { throw RXLanguageError.invalid("IDs must be positive.") } }
        return "{" + (try self.name(name)) + "}" + (localID.map { "[\($0)]" } ?? "") + " = [" + (try self.value(value)) + "]" + (globalID.map { "[\($0)]" } ?? "") + ","
    }
    public static func template() -> String {
        """
        /- Red-XAI database. Key references are public identifiers, not API secrets.
        {Red-XAI}[1]{
            {Profile}[2]{
                {Name}[1] = ["Player"][1],
                {Verified}[2] = [False][2],
                {Settings}[3] = [table[Theme = "Blood", Volume = 0.8]][3],
                {Inventory}[4] = [["Mask", 3, True, NELL]][4],
            <[False,2,false]>}
        <[False,1,false,"keyref:local"]>}

        """
    }
    /// Re-indents existing lines without discarding comments or changing values.
    public static func indent(_ snapshot: RXLanguageSnapshot) throws -> String {
        guard snapshot.isValid else { throw RXLanguageError.invalid("Resolve syntax errors before re-indenting.") }
        let lines = snapshot.source.components(separatedBy: "\n")
        let comments = snapshot.tokens.filter { $0.kind == .comment }.map(\.span)
        let events = snapshot.boxes.flatMap { [($0.span.offset + 1, 1), ($0.insertionOffset, -1)] }.sorted { $0.0 < $1.0 }
        var position = 0, eventIndex = 0, commentIndex = 0, depth = 0
        let result = lines.map { raw -> String in
            defer { position += raw.utf16.count + 1 }
            let trimmed = raw.trimmingCharacters(in: .whitespaces)
            guard !trimmed.isEmpty else { return "" }
            let first = position + raw.prefix { $0 == " " || $0 == "\t" }.utf16.count
            while eventIndex < events.count && events[eventIndex].0 <= first { depth += events[eventIndex].1; eventIndex += 1 }
            while commentIndex < comments.count && comments[commentIndex].end <= first { commentIndex += 1 }
            if commentIndex < comments.count && comments[commentIndex].offset < first && comments[commentIndex].end > first { return raw }
            return String(repeating: "    ", count: min(max(0, depth), RedXAILanguage.maximumDepth)) + trimmed
        }.joined(separator: "\n")
        guard result.utf8.count <= RedXAIValidator.maxBytes else { throw RXLanguageError.limit }
        return result
    }
}
