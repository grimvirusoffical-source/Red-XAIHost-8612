import Foundation
import XCTest
@testable import RedXAICore

final class RXEngineTests: XCTestCase {
    func document(_ body: String) -> String { "{Red-XAI}[1]{\n" + body + "\n<[False,1,false,\"keyref:test\"]>}" }
    func parse(_ raw: String) -> RXLanguageSnapshot { RedXAILanguage.inspect(document(raw), strict: true) }
    func assertValid(_ snapshot: RXLanguageSnapshot, file: StaticString = #filePath, line: UInt = #line) {
        XCTAssertTrue(snapshot.isValid, snapshot.diagnostics.map(\.message).joined(separator: "\n"), file: file, line: line)
    }
    func testValueCannotInjectAdditionalStatements() { XCTAssertThrowsError(try RedXAILanguage.parseValue("1],\n{Injected}=[2")) }
    func testNestedRedXAIRootRejected() { XCTAssertFalse(parse("{Red-XAI}[2]{<[False,2,false,\"keyref:x\"]>}").isValid) }
    func testEditAccessMetadata() throws {
        let s = parse("")
        let next = try RXMutations.setBoxAccess(boxID: s.boxes[0].id, crossDatabase: false, globalID: 1, intraFile: true, keyReference: "keyref:changed", snapshot: s, currentSource: s.source)
        let parsed = RedXAILanguage.inspect(next, strict: true); assertValid(parsed)
        XCTAssertEqual(parsed.boxes[0].access?.keyReference, "keyref:changed")
        XCTAssertEqual(parsed.boxes[0].access?.intraFile, true)
    }
    func testTemplateStrictlyValid() { assertValid(RedXAILanguage.inspect(RXSerializer.template(), strict: true)) }
    func testNestedBoxesAndAccess() {
        let s = parse("{A}[2]{\n{B}[3]{\n{Age}[1] = [27][4],\n<[GAccsess,ReadProfile,1001,]>\n<[True,3,true]>}\n<[False,2,false]>}")
        assertValid(s); XCTAssertEqual(s.boxes.count, 3); XCTAssertEqual(s.packers[0].path, "/Red-XAI/A/B/Age")
        XCTAssertEqual(s.boxes.last?.access?.crossDatabase, true)
    }
    func testScalars() throws {
        for (text, expected) in [("12", RXValue.number(12)), ("-1.25e2", .number(-125)), ("TRUE", .boolean(true)), ("false", .boolean(false)), ("NeLl", .nell), (#""héllo 🐈""#, .string("héllo 🐈"))] {
            XCTAssertEqual(try RedXAILanguage.parseValue(text), expected)
        }
    }
    func testEscapedStrings() throws { XCTAssertEqual(try RedXAILanguage.parseValue(#""a\n\"b\\\u00e9""#), .string("a\n\"b\\é")) }
    func testCommasQuotesAndEqualsAreNotSeparators() throws {
        XCTAssertEqual(try RedXAILanguage.parseValue(#"["a,b", "c=d", "[{}]"]"#), .array([.string("a,b"), .string("c=d"), .string("[{}]")]))
    }
    func testStringArrayNotMistakenForOneString() throws {
        XCTAssertEqual(try RedXAILanguage.parseValue(#""a","b""#), .array([.string("a"), .string("b")]))
    }
    func testNestedArraysTablesAndMetadata() throws {
        let value = try RedXAILanguage.parseValue(#"table[Audio = [Volume = 0.8, Muted = False], Rows = [[1,2],[3]], Meta = meta[__type = "Player"]]"#)
        if case .table(let entries) = value { XCTAssertEqual(entries.count, 3); XCTAssertEqual(entries[2].value.typeName, "Metatable") } else { XCTFail() }
    }
    func testEmptyContainers() throws {
        XCTAssertEqual(try RedXAILanguage.parseValue("[]"), .array([]))
        XCTAssertEqual(try RedXAILanguage.parseValue("table[]"), .table([]))
        XCTAssertEqual(try RedXAILanguage.parseValue("meta[]"), .metatable([]))
    }
    func testSingletonArraySurvivesRoundtrip() throws { XCTAssertEqual(try RedXAILanguage.parseValue(RXSerializer.value(.array([.number(1)]))), .array([.number(1)])) }
    func testDuplicateTableKeyRejected() { XCTAssertThrowsError(try RedXAILanguage.parseValue("table[A=1,A=2]")) }
    func testMalformedAndNonFiniteNumbersRejected() {
        for text in ["nan", "Infinity", "1e9999", "1.2.3", "01", "--1", "1e+"] { XCTAssertThrowsError(try RedXAILanguage.parseValue(text), text) }
    }
    func testInvalidEscapeRejected() { XCTAssertThrowsError(try RedXAILanguage.parseValue(#""bad\q""#)) }
    func testIDsOptionalAndFalse() { let s = parse("{A}[False] = [1][],\n{B} = [2],"); assertValid(s); XCTAssertNil(s.packers[0].localID); XCTAssertNil(s.packers[1].globalID) }
    func testMalformedIDsAreNotSilentlyIgnored() {
        for id in ["banana", "-1", "1.2", "True", "0", "9999999999999999999999999"] { XCTAssertFalse(parse("{A}[\(id)] = [1],").isValid, id) }
    }
    func testDuplicateLocalIDScope() {
        let s = parse("{A}[2]{ {P}[1] = [1], <[False,2,false]>}\n{B}[3]{ {P}[1] = [2], <[False,3,false]>}")
        assertValid(s); XCTAssertEqual(s.find(localID: 1).count, 2)
    }
    func testDuplicateLocalPackerRejectedInSameBox() { XCTAssertFalse(parse("{P}[1] = [1],\n{P}[1] = [2],").isValid) }
    func testNamesWithSameIDAllowed() { assertValid(parse("{A}[2] = [1][2],\n{B}[2] = [2][2],")) }
    func testDuplicateGlobalPackerRejectedAcrossBoxes() {
        XCTAssertFalse(parse("{A}[2]{ {P}[1] = [1][8], <[False,2,false]>}\n{B}[3]{ {P}[1] = [2][8], <[False,3,false]>}").isValid)
    }
    func testDuplicateBoxLocalAndGlobalIDs() {
        XCTAssertFalse(parse("{A}[2]{<[False,2,false]>}\n{B}[2]{<[False,2,false]>}").isValid)
    }
    func testQuickFind() { let s = parse("{PlayerName}[2]=[\"A\"][9],\n{PlayerAge}[3]=[27][10],"); XCTAssertEqual(s.find(name: "player").count, 2); XCTAssertEqual(s.find(localID: 2).first?.name, "PlayerName"); XCTAssertEqual(s.find(globalID: 10).first?.name, "PlayerAge") }
    func testCaseSensitiveNames() { assertValid(parse("{A}[1]=[1][1],\n{a}[1]=[2][1],")) }
    func testRootInsideStringOrCommentDoesNotCount() { XCTAssertFalse(RedXAILanguage.inspect(#"/- {Red-XAI}[1]{"#).isValid) }
    func testUnexpectedClosersBalancedLaterStillRejected() { XCTAssertFalse(RedXAILanguage.inspect("}{Red-XAI}[1]{<[False,1,false,\"keyref:x\"]>}").isValid) }
    func testWrongRootID() { XCTAssertFalse(RedXAILanguage.inspect("{Red-XAI}[2]{<[False,1,false,\"keyref:x\"]>}").isValid) }
    func testMissingComma() { XCTAssertFalse(parse("{P}[1]=[1]").isValid) }
    func testSingleLineCommentsIgnoreBrackets() { assertValid(parse("/- ] } == broken quote \"\n{P}[1]=[1], /- inline } ]")) }
    func testMultilineComments() { assertValid(parse("/-\n} ] = \"\n-\\\n{P}[1]=[1],")) }
    func testUnterminatedBlockComment() { XCTAssertFalse(parse("/-\nthis never closes").isValid) }
    func testInlineClosedComment() { assertValid(parse("/- inline comment -\\ {P}[1]=[1],")) }
    func testCommentsInsideValues() { assertValid(parse("{P}[1] = [\n1, /- comment\n2\n],")) }
    func testLegacyClosersWarnRatherThanInventAccess() { let s = RedXAILanguage.inspect("{Red-XAI}[1]{\n{P}[1]=[1],\n}"); XCTAssertTrue(s.isValid); XCTAssertTrue(s.diagnostics.contains { $0.severity == .warning }); XCTAssertNil(s.roots.first?.access) }
    func testStrictModeRejectsMissingCloser() { XCTAssertFalse(RedXAILanguage.inspect("{Red-XAI}[1]{}", strict: true).isValid) }
    func testUnicodeRangesSelectActualValue() {
        let s = parse("{\"名前🐈\"}[1] = [\"hello🐈\"],"); assertValid(s)
        XCTAssertEqual((s.source as NSString).substring(with: s.packers[0].valueSpan.range), #""hello🐈""#)
    }
    func testDiagnosticsDeterministic() { let text = document("{A}[bad] = ["), a = RedXAILanguage.inspect(text), b = RedXAILanguage.inspect(text); XCTAssertEqual(a.diagnostics, b.diagnostics) }
    func testDepthBound() { XCTAssertFalse(parse("{P}=[" + String(repeating: "[", count: 100) + "1" + String(repeating: "]", count: 100) + "],").isValid) }
    func testSizeBound() { XCTAssertFalse(RedXAILanguage.inspect(String(repeating: "x", count: RedXAIValidator.maxBytes + 1)).isValid) }
    func testNoUnboundedDiagnostics() { let s = RedXAILanguage.inspect(String(repeating: "?", count: 2000)); XCTAssertLessThanOrEqual(s.diagnostics.count, 100) }
    func testMutationPreservesComments() throws {
        let s = parse("/- preserve me\n{P}[1] = [1],")
        let updated = try RXMutations.setValue(.string("🐈"), packerID: s.packers[0].id, snapshot: s, currentSource: s.source)
        XCTAssertTrue(updated.contains("/- preserve me")); XCTAssertEqual(RedXAILanguage.inspect(updated).packers[0].value, .string("🐈"))
    }
    func testStaleMutationRefusesToOverwrite() { let s = parse("{P}=[1],"); XCTAssertThrowsError(try RXMutations.setValue(.number(2), packerID: s.packers[0].id, snapshot: s, currentSource: s.source + " ")) }
    func testAddRemoveBoxAndPacker() throws {
        let s = parse("")
        let withBox = try RXMutations.addBox(name: "Players", localID: 2, globalID: 2, boxID: s.boxes[0].id, snapshot: s, currentSource: s.source)
        let next = RedXAILanguage.inspect(withBox, strict: true); assertValid(next)
        let withValue = try RXMutations.addPacker(name: "Age", localID: 1, globalID: 1, value: .number(28), boxID: next.boxes[1].id, snapshot: next, currentSource: next.source)
        let filled = RedXAILanguage.inspect(withValue); assertValid(filled); XCTAssertEqual(filled.packers.count, 1)
        let removed = try RXMutations.remove(filled.packers[0].id, snapshot: filled, currentSource: filled.source)
        XCTAssertEqual(RedXAILanguage.inspect(removed).packers.count, 0)
    }
    func testAddGrantIsMetadataNotSecret() throws { let s = parse(""); let next = try RXMutations.addAccess(name: "Reader", tokenID: 9, boxID: s.boxes[0].id, snapshot: s, currentSource: s.source); assertValid(RedXAILanguage.inspect(next, strict: true)) }
    func testNestedValueMutation() throws { let v = RXValue.table([.init(key: "a", value: .array([.number(1)]))]); let n = try RXMutations.replacing(v, at: [.key("a"), .index(0)], with: .number(9)); XCTAssertEqual(n, .table([.init(key: "a", value: .array([.number(9)]))])) }
    func testInvalidMutationRollsBack() { let s = parse("{A}[1]=[1],"); XCTAssertThrowsError(try RXMutations.addPacker(name: "A", localID: 1, globalID: nil, value: .number(2), boxID: s.boxes[0].id, snapshot: s, currentSource: s.source)) }
    func testIndentKeepsCommentsAndValues() throws { let s = parse("/- unchanged comment\n{P}=[table[A=1]],"); let next = try RXSerializer.indent(s); XCTAssertTrue(next.contains("unchanged comment")); XCTAssertEqual(RedXAILanguage.inspect(next).packers.map(\.value), s.packers.map(\.value)) }
    func testThemeDefaultsAndRoundtrip() throws { for theme in [RXEditorTheme.dark, .light] { try theme.validate(); XCTAssertEqual(try RXEditorTheme.importingCSS(theme.cssTemplate(), name: theme.name, appearance: theme.appearance), theme) } }
    func testThemeRejectsExecutableCSS() { for source in ["@import url(https://x);", ":root { --rx-background: url(x); }", "body { color:red; }"] { XCTAssertThrowsError(try RXEditorTheme.importingCSS(source, name: "Bad", appearance: "dark")) } }
    func testThemeRejectsInvisibleText() { var theme = RXEditorTheme.dark; theme.colors["foreground"] = theme.colors["background"]; XCTAssertThrowsError(try theme.validate()) }
    func testSerializedValuesRoundTrip() throws {
        var seed: UInt64 = 17
        func random(_ max: Int) -> Int { seed = seed &* 6364136223846793005 &+ 1; return Int((seed >> 33) % UInt64(max)) }
        func generate(_ depth: Int) -> RXValue {
            switch random(depth < 3 ? 7 : 4) {
            case 0: return .number(Double(random(10000)) / 10)
            case 1: return .string(["hello", "a,b", "é🐈", "\n\"\\"][random(4)])
            case 2: return .boolean(random(2) == 0)
            case 3: return .nell
            case 4: return .array((0..<random(4)).map { _ in generate(depth + 1) })
            case 5: return .table((0..<random(4)).map { .init(key: "K\($0)", value: generate(depth + 1)) })
            default: return .metatable([.init(key: "Type", value: .string("Record"))])
            }
        }
        for _ in 0..<300 { let value = generate(0); XCTAssertEqual(try RedXAILanguage.parseValue(RXSerializer.value(value)), value) }
    }
    func testMalformedCorpusTerminates() {
        let pieces = ["{", "}", "[", "]", "=", "\"", ",", "<", ">", "A", "1", "meta", "NELL", "/-\n", "-\\"]
        var seed: UInt64 = 91
        for _ in 0..<500 {
            var source = ""
            for _ in 0..<60 { seed = seed &* 2862933555777941757 &+ 3037000493; source += pieces[Int((seed >> 33) % UInt64(pieces.count))] + " " }
            let result = RedXAILanguage.inspect(source); XCTAssertLessThanOrEqual(result.diagnostics.count, 100)
        }
    }
}
