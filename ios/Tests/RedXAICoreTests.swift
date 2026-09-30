import XCTest
@testable import RedXAICore

final class RedXAICoreTests:XCTestCase{
    func testValidDocument(){
        let s="{Red-XAI}[1]{\n{PInfo}[2]{\n{Player}[1] = [True][2],\n<[True,3,false]>}\n<[True,1,false,\"keyref:x\"]>}\n"
        XCTAssertTrue(RedXAIValidator.validate(s).isValid)
    }
    func testMissingRoot(){XCTAssertFalse(RedXAIValidator.validate("{PInfo}[2]{}").isValid)}
    func testMissingComma(){
        let s="{Red-XAI}[1]{\n{Player}[1] = [True][2]\n<[True,1,false,\"keyref:x\"]>}\n"
        XCTAssertFalse(RedXAIValidator.validate(s).isValid)
    }
    func testUnterminatedString(){
        let s="{Red-XAI}[1]{\n{Player}[1] = [\"oops][2],\n<[True,1,false,\"keyref:x\"]>}\n"
        XCTAssertFalse(RedXAIValidator.validate(s).isValid)
    }
    func testOversize(){XCTAssertFalse(RedXAIValidator.validate("{Red-XAI}[1]{"+String(repeating:"x",count:RedXAIValidator.maxBytes)+"}").isValid)}
    func testUnexpectedClosingBrace(){
        let result=RedXAIValidator.validate("{Red-XAI}[1]{} }")
        XCTAssertFalse(result.isValid)
        XCTAssertTrue(result.diagnostics.contains{$0.message.contains("closing brace")})
    }
    func testUnexpectedClosingBracket(){
        let result=RedXAIValidator.validate("{Red-XAI}[1]{\n]}\n")
        XCTAssertFalse(result.isValid)
        XCTAssertTrue(result.diagnostics.contains{$0.message.contains("closing bracket")})
    }
}
