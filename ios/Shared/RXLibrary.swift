import Foundation
#if canImport(Darwin)
import Darwin
#elseif canImport(Glibc)
import Glibc
#endif

public struct RXDatabaseRecord: Identifiable, Codable, Equatable, Sendable {
    public var id: UUID
    public var schema: Int
    public var name: String
    public var source: String
    public var revision: Int
    public var createdAt: Date
    public var modifiedAt: Date
    public var operation: String
}
public struct RXLibraryListing: Sendable {
    public var records: [RXDatabaseRecord]
    public var unreadableCount: Int
}
public enum RXLibraryError: Error, LocalizedError, Sendable {
    case invalidName, duplicateName, missing, conflict, corrupt, quota, locked
    public var errorDescription: String? {
        switch self {
        case .invalidName: return "Choose a name of 1–80 characters without path separators or control characters."
        case .duplicateName: return "A database with this name already exists. Choose a different name."
        case .missing: return "This database is no longer available. Refresh the library."
        case .conflict: return "A newer revision is already saved. Your edits are still in the editor; export them before reloading."
        case .corrupt: return "The saved record could not be read safely. It has not been overwritten."
        case .quota: return "The local library limit was reached. Export or remove unused databases and backups."
        case .locked: return "The local library is busy or cannot be opened. Try again."
        }
    }
}

/// Serializes edits and uses an OS file lock for cooperation between library instances.
/// This is local durable storage, not a network server or an authentication database.
public actor RXLibrary {
    public static let maximumDatabases = 200
    public static let retainedRevisions = 20
    public static let maximumStorageBytes = 256 * 1024 * 1024
    private let root: URL
    private let fm = FileManager.default
    private var active: URL { root.appendingPathComponent("Databases", isDirectory: true) }
    private var trash: URL { root.appendingPathComponent("Trash", isDirectory: true) }
    public init(root: URL) { self.root = root }

    public static func normalizedName(_ input: String) throws -> String {
        var name = input.precomposedStringWithCanonicalMapping.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !name.isEmpty, name.count <= 120,
              !name.unicodeScalars.contains(where: { CharacterSet.controlCharacters.contains($0) }),
              name.rangeOfCharacter(from: CharacterSet(charactersIn: "/\\:<>\"|?*")) == nil,
              !name.hasPrefix(".") else { throw RXLibraryError.invalidName }
        if let dot = name.lastIndex(of: ".") { name = String(name[..<dot]) }
        name = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !name.isEmpty, name.count <= 80, !name.hasSuffix(".") else { throw RXLibraryError.invalidName }
        return name + ".Red-XAI"
    }

    public func list(inTrash: Bool = false) throws -> RXLibraryListing {
        try locked { try listing(inTrash ? trash : active) }
    }
    public func read(_ id: UUID, inTrash: Bool = false) throws -> RXDatabaseRecord {
        try locked { try load(directory(id, inTrash: inTrash).appendingPathComponent("current.json"), expectedID: id) }
    }
    public func create(name: String, source: String = RXSerializer.template()) throws -> RXDatabaseRecord {
        try locked { try createLocked(name: name, source: source) }
    }
    public func save(_ id: UUID, source: String, expectedRevision: Int, name: String? = nil) throws -> RXDatabaseRecord {
        try locked { try saveLocked(id, source: source, expectedRevision: expectedRevision, name: name, operation: "Saved") }
    }
    public func duplicate(_ id: UUID) throws -> RXDatabaseRecord {
        try locked {
            let record = try load(directory(id).appendingPathComponent("current.json"), expectedID: id)
            let base = String(record.name.dropLast(".Red-XAI".count))
            let names = Set(try listing(active).records.map { $0.name.lowercased() })
            var proposed = String(base.prefix(64)) + " Copy"
            var counter = 2
            while names.contains((proposed + ".Red-XAI").lowercased()) {
                proposed = String(base.prefix(60)) + " Copy \(counter)"; counter += 1
            }
            return try createLocked(name: proposed, source: record.source)
        }
    }
    public func moveToTrash(_ id: UUID) throws {
        try locked {
            let from = directory(id), to = directory(id, inTrash: true)
            guard fm.fileExists(atPath: from.path) else { throw RXLibraryError.missing }
            try fm.moveItem(at: from, to: to)
        }
    }
    public func recover(_ id: UUID) throws {
        try locked {
            let record = try load(directory(id, inTrash: true).appendingPathComponent("current.json"), expectedID: id)
            try checkName(record.name, excluding: nil)
            try fm.moveItem(at: directory(id, inTrash: true), to: directory(id))
        }
    }
    public func permanentlyDelete(_ id: UUID) throws {
        try locked {
            let path = directory(id, inTrash: true)
            guard fm.fileExists(atPath: path.path) else { throw RXLibraryError.missing }
            try fm.removeItem(at: path)
        }
    }
    public func revisions(_ id: UUID) throws -> [RXDatabaseRecord] {
        try locked {
            let folder = directory(id).appendingPathComponent("History", isDirectory: true)
            guard fm.fileExists(atPath: folder.path) else { return [] }
            let files = try fm.contentsOfDirectory(at: folder, includingPropertiesForKeys: nil)
            guard files.count <= Self.retainedRevisions + 2 else { throw RXLibraryError.corrupt }
            return try files.filter { $0.pathExtension == "json" }.map { try load($0, expectedID: id) }.sorted { $0.revision > $1.revision }
        }
    }
    public func restore(_ id: UUID, revision: Int, expectedRevision: Int) throws -> RXDatabaseRecord {
        guard revision > 0 else { throw RXLibraryError.corrupt }
        return try locked {
            let path = directory(id).appendingPathComponent("History/\(revision).json")
            let old = try load(path, expectedID: id)
            return try saveLocked(id, source: old.source, expectedRevision: expectedRevision, name: nil, operation: "Restored revision \(revision)")
        }
    }

    private func directory(_ id: UUID, inTrash: Bool = false) -> URL {
        (inTrash ? trash : active).appendingPathComponent(id.uuidString, isDirectory: true)
    }
    private func locked<T>(_ action: () throws -> T) throws -> T {
        try fm.createDirectory(at: active, withIntermediateDirectories: true)
        try fm.createDirectory(at: trash, withIntermediateDirectories: true)
        let descriptor = open(root.appendingPathComponent(".library.lock").path, O_CREAT | O_RDWR | O_NOFOLLOW, 0o600)
        guard descriptor >= 0 else { throw RXLibraryError.locked }
        defer { _ = close(descriptor) }
        guard flock(descriptor, LOCK_EX) == 0 else { throw RXLibraryError.locked }
        defer { _ = flock(descriptor, LOCK_UN) }
        return try action()
    }
    private func listing(_ folder: URL) throws -> RXLibraryListing {
        let children = try fm.contentsOfDirectory(at: folder, includingPropertiesForKeys: [.isSymbolicLinkKey])
        guard children.count <= Self.maximumDatabases * 2 + 10 else { throw RXLibraryError.quota }
        var records: [RXDatabaseRecord] = [], unreadable = 0
        for child in children {
            guard let id = UUID(uuidString: child.lastPathComponent) else { continue }
            do {
                guard try child.resourceValues(forKeys: [.isSymbolicLinkKey]).isSymbolicLink != true else { throw RXLibraryError.corrupt }
                records.append(try load(child.appendingPathComponent("current.json"), expectedID: id))
            } catch { unreadable += 1 }
        }
        return .init(records: records.sorted { $0.modifiedAt > $1.modifiedAt }, unreadableCount: unreadable)
    }
    private func load(_ url: URL, expectedID: UUID) throws -> RXDatabaseRecord {
        guard fm.fileExists(atPath: url.path) else { throw RXLibraryError.missing }
        let values = try url.resourceValues(forKeys: [.fileSizeKey, .isSymbolicLinkKey])
        guard values.isSymbolicLink != true, let size = values.fileSize, size <= RedXAIValidator.maxBytes * 7 + 16_384 else { throw RXLibraryError.corrupt }
        do {
            let result = try JSONDecoder().decode(RXDatabaseRecord.self, from: Data(contentsOf: url))
            guard result.schema == 1, result.id == expectedID, result.revision > 0,
                  try Self.normalizedName(result.name) == result.name,
                  result.source.utf8.count <= RedXAIValidator.maxBytes else { throw RXLibraryError.corrupt }
            return result
        } catch { throw RXLibraryError.corrupt }
    }
    private func checkName(_ name: String, excluding id: UUID?) throws {
        let listing = try listing(active)
        guard listing.unreadableCount == 0 else { throw RXLibraryError.corrupt }
        guard !listing.records.contains(where: { $0.id != id && $0.name.lowercased() == name.lowercased() }) else { throw RXLibraryError.duplicateName }
    }
    private func checkQuota(extra: Int) throws {
        var total = extra
        if let paths = fm.enumerator(at: root, includingPropertiesForKeys: [.fileSizeKey, .isRegularFileKey, .isSymbolicLinkKey]) {
            var count = 0
            for case let url as URL in paths {
                count += 1; guard count <= 12_000 else { throw RXLibraryError.quota }
                let values = try url.resourceValues(forKeys: [.fileSizeKey, .isRegularFileKey, .isSymbolicLinkKey])
                if values.isSymbolicLink == true { paths.skipDescendants(); continue }
                if values.isRegularFile == true { total += values.fileSize ?? 0 }
                guard total <= Self.maximumStorageBytes else { throw RXLibraryError.quota }
            }
        }
    }
    private func write(_ record: RXDatabaseRecord, to url: URL) throws {
        let encoder = JSONEncoder(); encoder.outputFormatting = [.sortedKeys]
        let data = try encoder.encode(record)
        #if os(iOS)
        try data.write(to: url, options: [.atomic, .completeFileProtection])
        #else
        try data.write(to: url, options: .atomic)
        #endif
    }
    private func createLocked(name: String, source: String) throws -> RXDatabaseRecord {
        _ = try RXDocumentCodec.encode(source)
        let name = try Self.normalizedName(name)
        try checkName(name, excluding: nil)
        let existing = try listing(active)
        guard existing.records.count < Self.maximumDatabases else { throw RXLibraryError.quota }
        try checkQuota(extra: source.utf8.count * 7 + 16_384)
        let id = UUID(), now = Date()
        let record = RXDatabaseRecord(id: id, schema: 1, name: name, source: source, revision: 1, createdAt: now, modifiedAt: now, operation: "Created")
        let staging = active.appendingPathComponent(".new-" + id.uuidString, isDirectory: true)
        try fm.createDirectory(at: staging, withIntermediateDirectories: true)
        do { try write(record, to: staging.appendingPathComponent("current.json")); try fm.moveItem(at: staging, to: directory(id)) }
        catch { try? fm.removeItem(at: staging); throw error }
        return record
    }
    private func saveLocked(_ id: UUID, source: String, expectedRevision: Int, name: String?, operation: String) throws -> RXDatabaseRecord {
        _ = try RXDocumentCodec.encode(source)
        let url = directory(id).appendingPathComponent("current.json")
        let old = try load(url, expectedID: id)
        guard expectedRevision == old.revision else { throw RXLibraryError.conflict }
        let chosen = try Self.normalizedName(name ?? old.name)
        try checkName(chosen, excluding: id)
        if old.source == source && old.name == chosen { return old }
        guard old.revision < Int.max else { throw RXLibraryError.quota }
        try checkQuota(extra: (source.utf8.count + old.source.utf8.count) * 7 + 32_768)
        var next = old; next.source = source; next.name = chosen; next.revision += 1; next.modifiedAt = Date(); next.operation = operation
        let history = directory(id).appendingPathComponent("History", isDirectory: true)
        try fm.createDirectory(at: history, withIntermediateDirectories: true)
        try write(old, to: history.appendingPathComponent("\(old.revision).json"))
        try write(next, to: url)
        let paths = (try? fm.contentsOfDirectory(at: history, includingPropertiesForKeys: nil)) ?? []
        let sorted = paths.filter { Int($0.deletingPathExtension().lastPathComponent) != nil }.sorted {
            (Int($0.deletingPathExtension().lastPathComponent) ?? 0) > (Int($1.deletingPathExtension().lastPathComponent) ?? 0)
        }
        for path in sorted.dropFirst(Self.retainedRevisions) { try? fm.removeItem(at: path) }
        return next
    }
}
