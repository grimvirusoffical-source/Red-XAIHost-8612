import Foundation
import SwiftUI
import RedXAICore

@MainActor
final class HostDraftStore: ObservableObject {
    @Published private(set) var drafts: [RXNodeDraft] = []
    @Published private(set) var ready = false
    @Published var issue: String?
    private var fileURL: URL?

    init() {
        do {
            guard let root = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask).first else {
                throw RXNodeDraftError.invalidData
            }
            let directory = root.appendingPathComponent("RedXAI", isDirectory: true)
            let url = directory.appendingPathComponent("node-drafts-v1.json")
            fileURL = url
            if FileManager.default.fileExists(atPath: url.path) {
                let size = try url.resourceValues(forKeys: [.fileSizeKey]).fileSize ?? 0
                guard size <= RXNodeDraftCodec.maximumBytes else { throw RXNodeDraftError.invalidData }
                drafts = try RXNodeDraftCodec.decode(Data(contentsOf: url))
            }
            ready = true
        } catch {
            issue = "Local drafts could not be loaded. The existing file has been preserved; editing is disabled to prevent data loss."
        }
    }

    func add(name: String, kind: RXNodeKind) -> Bool {
        persist(drafts + [RXNodeDraft(name: name, kind: kind)])
    }

    func remove(id: UUID) {
        _ = persist(drafts.filter { $0.id != id })
    }

    private func persist(_ next: [RXNodeDraft]) -> Bool {
        guard ready, let fileURL else { return false }
        do {
            let data = try RXNodeDraftCodec.encode(next)
            try FileManager.default.createDirectory(at: fileURL.deletingLastPathComponent(), withIntermediateDirectories: true)
            try data.write(to: fileURL, options: [.atomic, .completeFileProtection])
            drafts = next
            return true
        } catch {
            issue = "The draft could not be saved. Previously saved drafts were not replaced."
            return false
        }
    }
}
