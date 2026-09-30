import Foundation
import XCTest
@testable import RedXAICore

final class RXLibraryTests: XCTestCase, @unchecked Sendable {
    func workspace() -> URL { FileManager.default.temporaryDirectory.appendingPathComponent("redxai-tests-" + UUID().uuidString) }
    func testNameNormalization() throws {
        XCTAssertEqual(try RXLibrary.normalizedName(" Accounts.txt "), "Accounts.Red-XAI")
        XCTAssertEqual(try RXLibrary.normalizedName("Players"), "Players.Red-XAI")
        XCTAssertEqual(try RXLibrary.normalizedName("Players.Red-XAI"), "Players.Red-XAI")
        for name in ["", ".", "..", "../secret", "A/B", "A\\B", "A:B", ".hidden", String(repeating: "a", count: 81)] { XCTAssertThrowsError(try RXLibrary.normalizedName(name), name) }
    }
    func testCreateSaveReopenHistoryRestore() async throws {
        let root = workspace(); defer { try? FileManager.default.removeItem(at: root) }
        let library = RXLibrary(root: root)
        let record = try await library.create(name: "Accounts.txt")
        XCTAssertEqual(record.name, "Accounts.Red-XAI")
        let saved = try await library.save(record.id, source: record.source + "\n/- update", expectedRevision: 1)
        XCTAssertEqual(saved.revision, 2)
        let reopened = try await RXLibrary(root: root).read(record.id)
        XCTAssertEqual(reopened, saved)
        let history = try await library.revisions(record.id)
        XCTAssertEqual(history.map(\.revision), [1])
        let restored = try await library.restore(record.id, revision: 1, expectedRevision: 2)
        XCTAssertEqual(restored.source, record.source); XCTAssertEqual(restored.revision, 3)
    }
    func testRenamePreservesSource() async throws {
        let root = workspace(); defer { try? FileManager.default.removeItem(at: root) }
        let library = RXLibrary(root: root), record = try await library.create(name: "A")
        let next = try await library.save(record.id, source: record.source, expectedRevision: record.revision, name: "B.csv")
        XCTAssertEqual(next.name, "B.Red-XAI"); XCTAssertEqual(next.source, record.source)
    }
    func testCaseInsensitiveDuplicateNameRejected() async throws {
        let root = workspace(); defer { try? FileManager.default.removeItem(at: root) }
        let library = RXLibrary(root: root)
        _ = try await library.create(name: "Accounts")
        do { _ = try await library.create(name: "accounts.txt"); XCTFail("Expected duplicate refusal") } catch {}
    }
    func testStaleRevisionDoesNotOverwrite() async throws {
        let root = workspace(); defer { try? FileManager.default.removeItem(at: root) }
        let library = RXLibrary(root: root), record = try await library.create(name: "A")
        _ = try await library.save(record.id, source: "new draft", expectedRevision: 1)
        do { _ = try await library.save(record.id, source: "stale", expectedRevision: 1); XCTFail() } catch {}
        let after = try await library.read(record.id); XCTAssertEqual(after.source, "new draft")
    }
    func testCorruptionIsNotSilentlyOverwritten() async throws {
        let root = workspace(); defer { try? FileManager.default.removeItem(at: root) }
        let library = RXLibrary(root: root), record = try await library.create(name: "A")
        let file = root.appendingPathComponent("Databases/\(record.id.uuidString)/current.json")
        let broken = Data("broken".utf8); try broken.write(to: file)
        do { _ = try await library.save(record.id, source: "oops", expectedRevision: 1); XCTFail() } catch {}
        XCTAssertEqual(try Data(contentsOf: file), broken)
        let listing = try await library.list(); XCTAssertEqual(listing.unreadableCount, 1)
    }
    func testTrashRecovery() async throws {
        let root = workspace(); defer { try? FileManager.default.removeItem(at: root) }
        let library = RXLibrary(root: root), record = try await library.create(name: "A")
        try await library.moveToTrash(record.id)
        let active = try await library.list(), trash = try await library.list(inTrash: true)
        XCTAssertEqual(active.records.count, 0); XCTAssertEqual(trash.records.map(\.id), [record.id])
        try await library.recover(record.id)
        let restored = try await library.read(record.id); XCTAssertEqual(restored.source, record.source)
    }
    func testPermanentDeleteOnlyAffectsTrash() async throws {
        let root = workspace(); defer { try? FileManager.default.removeItem(at: root) }
        let library = RXLibrary(root: root), record = try await library.create(name: "A")
        do { try await library.permanentlyDelete(record.id); XCTFail() } catch {}
        let kept = try await library.read(record.id); XCTAssertEqual(kept.id, record.id)
        try await library.moveToTrash(record.id); try await library.permanentlyDelete(record.id)
        let trash = try await library.list(inTrash: true); XCTAssertTrue(trash.records.isEmpty)
    }
    func testDuplicateRetainsOriginal() async throws {
        let root = workspace(); defer { try? FileManager.default.removeItem(at: root) }
        let library = RXLibrary(root: root), record = try await library.create(name: "A")
        let one = try await library.duplicate(record.id), two = try await library.duplicate(record.id)
        XCTAssertNotEqual(one.id, record.id); XCTAssertNotEqual(one.name, two.name); XCTAssertEqual(one.source, record.source)
    }
    func testHistoryRetentionAndNoOpSave() async throws {
        let root = workspace(); defer { try? FileManager.default.removeItem(at: root) }
        let library = RXLibrary(root: root); var record = try await library.create(name: "A")
        let unchanged = try await library.save(record.id, source: record.source, expectedRevision: record.revision)
        XCTAssertEqual(unchanged.revision, record.revision)
        for i in 0..<25 { record = try await library.save(record.id, source: "draft \(i)", expectedRevision: record.revision) }
        let revisions = try await library.revisions(record.id); XCTAssertEqual(revisions.count, 20); XCTAssertEqual(revisions.first?.revision, 25)
    }
    func testTwoInstancesRejectConcurrentStaleSave() async throws {
        let root = workspace(); defer { try? FileManager.default.removeItem(at: root) }
        let first = RXLibrary(root: root), second = RXLibrary(root: root)
        let record = try await first.create(name: "A")
        let count = await withTaskGroup(of: Bool.self, returning: Int.self) { group in
            group.addTask { do { _ = try await first.save(record.id, source: "first", expectedRevision: 1); return true } catch { return false } }
            group.addTask { do { _ = try await second.save(record.id, source: "second", expectedRevision: 1); return true } catch { return false } }
            var successes = 0; for await result in group { if result { successes += 1 } }; return successes
        }
        XCTAssertEqual(count, 1)
    }
    func testOversizedSavePreservesOriginal() async throws {
        let root = workspace(); defer { try? FileManager.default.removeItem(at: root) }
        let library = RXLibrary(root: root), record = try await library.create(name: "A")
        do { _ = try await library.save(record.id, source: String(repeating: "🐈", count: RedXAIValidator.maxBytes), expectedRevision: 1); XCTFail() } catch {}
        let after = try await library.read(record.id); XCTAssertEqual(after, record)
    }
}
