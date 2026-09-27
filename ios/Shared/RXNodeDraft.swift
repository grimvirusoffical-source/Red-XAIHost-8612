import Foundation

public enum RXNodeKind: String, Codable, CaseIterable, Identifiable, Sendable {
    case windows = "Windows"
    case macOS = "macOS"
    case linuxVPS = "Linux / VPS"
    public var id: String { rawValue }
}

/// Local planning data, never proof that a machine is connected or hosting workloads.
public struct RXNodeDraft: Identifiable, Codable, Equatable, Sendable {
    public let id: UUID
    public var name: String
    public var kind: RXNodeKind

    public init(id: UUID = UUID(), name: String, kind: RXNodeKind) {
        self.id = id
        self.name = name.trimmingCharacters(in: .whitespacesAndNewlines)
        self.kind = kind
    }
}

public enum RXNodeDraftError: Error, LocalizedError, Sendable {
    case invalidData
    public var errorDescription: String? {
        "The local draft data could not be read or saved. Existing drafts have not been overwritten."
    }
}

public enum RXNodeDraftCodec {
    public static let maximumDrafts = 64
    public static let maximumBytes = 128 * 1024
    private struct Envelope: Codable {
        let version: Int
        let drafts: [RXNodeDraft]
    }

    public static func validName(_ name: String) -> Bool {
        let trimmed = name.trimmingCharacters(in: .whitespacesAndNewlines)
        return !trimmed.isEmpty && trimmed.count <= 60 && !trimmed.unicodeScalars.contains {
            CharacterSet.controlCharacters.contains($0)
        }
    }

    private static func validate(_ drafts: [RXNodeDraft]) throws {
        guard drafts.count <= maximumDrafts,
              Set(drafts.map(\.id)).count == drafts.count,
              drafts.allSatisfy({ validName($0.name) }) else { throw RXNodeDraftError.invalidData }
    }

    public static func encode(_ drafts: [RXNodeDraft]) throws -> Data {
        try validate(drafts)
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.sortedKeys]
        let data = try encoder.encode(Envelope(version: 1, drafts: drafts))
        guard data.count <= maximumBytes else { throw RXNodeDraftError.invalidData }
        return data
    }

    public static func decode(_ data: Data) throws -> [RXNodeDraft] {
        guard data.count <= maximumBytes else { throw RXNodeDraftError.invalidData }
        let envelope = try JSONDecoder().decode(Envelope.self, from: data)
        guard envelope.version == 1 else { throw RXNodeDraftError.invalidData }
        try validate(envelope.drafts)
        return envelope.drafts
    }
}
