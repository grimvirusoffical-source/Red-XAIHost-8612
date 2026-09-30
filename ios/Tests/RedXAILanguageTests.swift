import XCTest
@testable import RedXAICore

final class RedXAILanguageTests:XCTestCase {
 func testValues(){
  let s="{Red-XAI}[1]{\n{Age}[2] = [27][3],\n{Name}[4] = [\"Player\"][5],\n{Verified}[6] = [TRUE][7],\n{Missing}[8] = [nell][9],\n{Items}[10] = [1,1.2,True,false,NELL][11],\n}"
  let r=RedXAILanguage.inspect(s)
  XCTAssertFalse(r.diagnostics.contains{$0.severity == .error}); XCTAssertEqual(r.packers.count,5)
  XCTAssertEqual(r.packers[0].value,.number(27)); XCTAssertEqual(r.packers[1].value,.string("Player")); XCTAssertEqual(r.packers[2].value,.boolean(true)); XCTAssertEqual(r.packers[3].value,.nell)
  if case .array(let v)=r.packers[4].value{XCTAssertEqual(v.count,5)}else{XCTFail()}
 }
 func testOptionalIDs(){let p=RedXAILanguage.inspect("{Red-XAI}[1]{\n{A}[False] = [1][],\n}").packers.first;XCTAssertNil(p?.localID);XCTAssertNil(p?.globalID)}
 func testDuplicateLocal(){XCTAssertTrue(RedXAILanguage.inspect("{Red-XAI}[1]{\n{A}[2] = [1][3],\n{A}[2] = [2][4],\n}").diagnostics.contains{$0.message.contains("Duplicate local")})}
 func testDuplicateGlobal(){XCTAssertTrue(RedXAILanguage.inspect("{Red-XAI}[1]{\n{A}[2] = [1][3],\n{A}[4] = [2][3],\n}").diagnostics.contains{$0.message.contains("Duplicate global")})}
 func testCommaInStringArray(){let p=RedXAILanguage.inspect("{Red-XAI}[1]{\n{A}[2] = [\"x,y\",2][3],\n}").packers.first;if case .array(let v)?=p?.value{XCTAssertEqual(v.count,2)}else{XCTFail()}}
 func testQuickFind(){
  let r=RedXAILanguage.inspect("{Red-XAI}[1]{\n{PlayerName}[2] = [\"A\"][9],\n{PlayerAge}[3] = [27][10],\n}")
  XCTAssertEqual(r.find(name:"player").count,2)
  XCTAssertEqual(r.find(localID:2).first?.name,"PlayerName")
  XCTAssertEqual(r.find(globalID:10).first?.name,"PlayerAge")
 }
}
