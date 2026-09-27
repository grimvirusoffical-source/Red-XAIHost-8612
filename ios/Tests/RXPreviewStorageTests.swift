import Foundation
import XCTest
@testable import RedXAICore

final class RXPreviewStorageTests: XCTestCase {
    func testUnicodeRoundTrip() throws {
        let text = "{Red-XAI}[1]{\n{Title}[2] = [\"日本語 🐈 café\"][2],\n<[True,1,false,\"keyref:test\"]>}"
        XCTAssertEqual(try RXDocumentCodec.decode(RXDocumentCodec.encode(text)), text)
    }
    func testInvalidUTF8IsRejected() {
        XCTAssertThrowsError(try RXDocumentCodec.decode(Data([0xFF, 0xFE, 0xFF]))) {
            XCTAssertEqual($0 as? RXDocumentIOError, .invalidUTF8)
        }
    }
    func testEmptyTextRoundTrip() throws {
        XCTAssertEqual(try RXDocumentCodec.decode(RXDocumentCodec.encode("")), "")
    }
    func testDocumentExactlyAtLimit() throws {
        let data = Data(repeating: 65, count: RedXAIValidator.maxBytes)
        XCTAssertEqual(try RXDocumentCodec.decode(data).utf8.count, RedXAIValidator.maxBytes)
    }
    func testOversizedImportRejected() {
        XCTAssertThrowsError(try RXDocumentCodec.decode(Data(repeating: 65, count: RedXAIValidator.maxBytes + 1)))
    }
    func testOversizedSaveRejected() {
        XCTAssertThrowsError(try RXDocumentCodec.encode(String(repeating: "x", count: RedXAIValidator.maxBytes + 1)))
    }
    func testUTF8LimitUsesBytesNotCharacters() {
        XCTAssertThrowsError(try RXDocumentCodec.encode(String(repeating: "🐈", count: RedXAIValidator.maxBytes / 4 + 1)))
    }
    func testDraftRoundTripPreservesIdentity() throws {
        let drafts = [RXNodeDraft(name: "My PC", kind: .windows), RXNodeDraft(name: "Mac Mini", kind: .macOS)]
        XCTAssertEqual(try RXNodeDraftCodec.decode(RXNodeDraftCodec.encode(drafts)), drafts)
    }
    func testEmptyDraftListRoundTrip() throws {
        XCTAssertEqual(try RXNodeDraftCodec.decode(RXNodeDraftCodec.encode([])), [])
    }
    func testDraftNameValidation() {
        XCTAssertFalse(RXNodeDraftCodec.validName("  "))
        XCTAssertFalse(RXNodeDraftCodec.validName(String(repeating: "a", count: 61)))
        XCTAssertFalse(RXNodeDraftCodec.validName("node\u{0000}name"))
        XCTAssertTrue(RXNodeDraftCodec.validName("My development PC"))
    }
    func testDuplicateDraftIDsRejected() {
        let draft = RXNodeDraft(name: "PC", kind: .windows)
        XCTAssertThrowsError(try RXNodeDraftCodec.encode([draft, draft]))
    }
    func testDraftLimit() throws {
        let drafts = (0..<64).map { RXNodeDraft(name: "Node \($0)", kind: .linuxVPS) }
        XCTAssertEqual(try RXNodeDraftCodec.decode(RXNodeDraftCodec.encode(drafts)).count, 64)
        XCTAssertThrowsError(try RXNodeDraftCodec.encode(drafts + [RXNodeDraft(name: "Extra", kind: .windows)]))
    }
    func testCorruptDraftDataRejected() {
        XCTAssertThrowsError(try RXNodeDraftCodec.decode(Data("broken".utf8)))
    }
    func testUnknownDraftSchemaRejected() {
        XCTAssertThrowsError(try RXNodeDraftCodec.decode(Data("{\"version\":2,\"drafts\":[]}".utf8)))
    }
    func testOversizedDraftDataRejected() {
        XCTAssertThrowsError(try RXNodeDraftCodec.decode(Data(repeating: 32, count: RXNodeDraftCodec.maximumBytes + 1)))
    }
}
