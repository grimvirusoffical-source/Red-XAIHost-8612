import Foundation
import SwiftUI
import RedXAICore

@MainActor
final class DatabaseApplication: ObservableObject {
    let library: RXLibrary
    @Published private(set) var records: [RXDatabaseRecord] = []
    @Published private(set) var discarded: [RXDatabaseRecord] = []
    @Published private(set) var loading = false
    @Published var issue: String?

    init() {
        let root: URL
        if ProcessInfo.processInfo.arguments.contains("--ui-testing") {
            let id = UUID(uuidString: ProcessInfo.processInfo.environment["RX_UI_LIBRARY"] ?? "") ?? UUID()
            root = FileManager.default.temporaryDirectory.appendingPathComponent("RedXAI-UI-" + id.uuidString)
        } else {
            root = URL.applicationSupportDirectory.appendingPathComponent("RedXAIDatabaseLibrary", isDirectory: true)
        }
        library = RXLibrary(root: root)
    }

    func refresh() async {
        loading = true
        defer { loading = false }
        do {
            let active = try await library.list(), trash = try await library.list(inTrash: true)
            records = active.records
            discarded = trash.records
            if active.unreadableCount + trash.unreadableCount > 0 {
                issue = "Some saved records could not be read. They have been preserved rather than silently replaced."
            }
        } catch { issue = error.localizedDescription }
    }

    func create(name: String, source: String) async -> UUID? {
        do {
            let result = try await library.create(name: name, source: source)
            await refresh()
            return result.id
        } catch { issue = error.localizedDescription; return nil }
    }
    func duplicate(_ id: UUID) async {
        do { _ = try await library.duplicate(id); await refresh() }
        catch { issue = error.localizedDescription }
    }
    func trash(_ id: UUID) async {
        do { try await library.moveToTrash(id); await refresh() }
        catch { issue = error.localizedDescription }
    }
    func recover(_ id: UUID) async {
        do { try await library.recover(id); await refresh() }
        catch { issue = error.localizedDescription }
    }
    func deletePermanently(_ id: UUID) async {
        do { try await library.permanentlyDelete(id); await refresh() }
        catch { issue = error.localizedDescription }
    }
    func importFile(_ url: URL) async {
        guard url.isFileURL else { issue = "Only local document files can be imported."; return }
        let accessed = url.startAccessingSecurityScopedResource()
        defer { if accessed { url.stopAccessingSecurityScopedResource() } }
        do {
            let size = try url.resourceValues(forKeys: [.fileSizeKey]).fileSize ?? 0
            guard size <= RedXAIValidator.maxBytes else { throw RXDocumentIOError.tooLarge }
            let source = try RXDocumentCodec.decode(Data(contentsOf: url))
            _ = try await library.create(name: url.lastPathComponent, source: source)
            await refresh()
        } catch { issue = "Import did not change the original file. " + error.localizedDescription }
    }
}

@MainActor
final class DatabaseSession: ObservableObject {
    @Published private(set) var record: RXDatabaseRecord
    @Published var text: String { didSet { if text != oldValue { changed() } } }
    @Published private(set) var snapshot: RXLanguageSnapshot?
    @Published private(set) var analyzing = false
    @Published private(set) var saving = false
    @Published var issue: String?
    @Published var storageConflict = false
    @Published var editorCommand = RXEditorCommand(.none)
    private let library: RXLibrary
    private let preferences: DatabasePreferences
    private var analysisTask: Task<Void, Never>?
    private var saveTask: Task<Void, Never>?
    private var writeTask: Task<Bool, Never>?
    var isDirty: Bool { text != record.source }
    var canEditStructure: Bool { snapshot?.source == text && snapshot?.isValid == true }

    init(record: RXDatabaseRecord, library: RXLibrary, preferences: DatabasePreferences) {
        self.record = record
        text = record.source
        self.library = library
        self.preferences = preferences
    }
    func begin() { analyze() }
    func analyze() {
        analysisTask?.cancel()
        let source = text
        analyzing = true
        analysisTask = Task {
            let result = await Task.detached(priority: .userInitiated) { RedXAILanguage.inspect(source) }.value
            guard !Task.isCancelled else { return }
            if text == source { snapshot = result }
            analyzing = false
        }
    }
    private func changed() {
        snapshot = nil
        analysisTask?.cancel()
        analyzing = false
        if text.utf8.count <= 512_000 {
            analysisTask = Task {
                do { try await Task.sleep(nanoseconds: 400_000_000) } catch { return }
                guard !Task.isCancelled else { return }
                analyze()
            }
        }
        scheduleSave()
    }
    private func scheduleSave() {
        saveTask?.cancel()
        guard preferences.autosave, !storageConflict else { return }
        saveTask = Task {
            do { try await Task.sleep(nanoseconds: 1_200_000_000) } catch { return }
            guard !Task.isCancelled else { return }
            await save()
        }
    }

    /// One writer owns a revision at a time. Explicit saves wait for that writer,
    /// then persist any newer text or rename instead of silently dropping them.
    @discardableResult
    func save(name: String? = nil) async -> Bool {
        saveTask?.cancel()
        saveTask = nil
        if let inFlight = writeTask {
            guard await inFlight.value else { return false }
            return await save(name: name)
        }
        guard !storageConflict else { return false }
        guard isDirty || name != nil else { return true }
        saving = true
        let operation = Task { @MainActor [self] () -> Bool in
            defer { saving = false; writeTask = nil }
            do {
                var requestedName = name
                repeat {
                    let content = text
                    let expected = record.revision
                    let updated = try await library.save(record.id, source: content,
                                                         expectedRevision: expected, name: requestedName)
                    record = updated
                    requestedName = nil
                    // Editing remains available during disk I/O. Drain the latest
                    // source before reporting success; never mark newer text saved.
                } while isDirty
                return true
            } catch {
                issue = error.localizedDescription
                if case RXLibraryError.conflict = error { storageConflict = true }
                return false
            }
        }
        writeTask = operation
        return await operation.value
    }

    func history() async throws -> [RXDatabaseRecord] { try await library.revisions(record.id) }
    func restore(_ revision: Int) async {
        guard await save(), !storageConflict else { return }
        guard !saving else { return }
        saving = true
        saveTask?.cancel()
        do {
            let restored = try await library.restore(record.id, revision: revision, expectedRevision: record.revision)
            record = restored
            text = restored.source
            storageConflict = false
            saving = false
            analyze()
        } catch { saving = false; issue = error.localizedDescription }
    }
    func mutate(_ operation: (RXLanguageSnapshot, String) throws -> String) {
        guard let snapshot, canEditStructure else {
            issue = "Validate the current document and resolve its errors first."
            return
        }
        do { text = try operation(snapshot, text); analyze() }
        catch { issue = error.localizedDescription }
    }
    func go(to offset: Int) { editorCommand = .init(.goToOffset(offset)) }
}
