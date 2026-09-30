import XCTest

@MainActor
final class DatabaseWorkspaceUITests: XCTestCase {
    override func setUpWithError() throws { continueAfterFailure = false }
    func testCreateStructureSaveAndReopen() throws {
        let app = XCUIApplication()
        app.launchArguments = ["--ui-testing"]
        app.launchEnvironment["RX_UI_LIBRARY"] = UUID().uuidString
        app.launch()
        XCTAssertTrue(app.buttons["createDatabase"].waitForExistence(timeout: 15))
        app.buttons["createDatabase"].tap()
        let field = app.textFields["databaseName"]
        XCTAssertTrue(field.waitForExistence(timeout: 5)); field.tap(); field.typeText("Smoke")
        app.buttons["confirmCreateDatabase"].tap()
        let editor = app.textViews["sourceEditor"]
        XCTAssertTrue(editor.waitForExistence(timeout: 15))
        XCTAssertTrue((editor.value as? String ?? "").contains("{Red-XAI}[1]"))
        app.buttons["databaseOutline"].tap()
        let boxMenu = app.buttons["boxMenu-Profile"]
        XCTAssertTrue(boxMenu.waitForExistence(timeout: 10)); boxMenu.tap()
        app.buttons["Add Packer"].tap()
        let name = app.textFields["structureName"]
        XCTAssertTrue(name.waitForExistence(timeout: 5)); name.tap(); name.typeText("BuildTest")
        app.buttons["applyStructure"].tap()
        let added = app.staticTexts["BuildTest"]
        XCTAssertTrue(added.waitForExistence(timeout: 10))
        app.navigationBars["Structure"].buttons["Done"].tap()
        XCTAssertTrue(editor.waitForExistence(timeout: 5))
        app.buttons["saveDatabase"].tap()
        let saved = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "Saved · revision 2")).firstMatch
        XCTAssertTrue(saved.waitForExistence(timeout: 10))
        XCTAssertTrue((editor.value as? String ?? "").contains("BuildTest"))
        let shot = XCTAttachment(screenshot: app.screenshot()); shot.name = "Database editor"; shot.lifetime = .keepAlways; add(shot)
        app.terminate(); app.launch()
        let record = app.staticTexts["Smoke.Red-XAI"]
        XCTAssertTrue(record.waitForExistence(timeout: 10)); record.tap()
        XCTAssertTrue(editor.waitForExistence(timeout: 10))
        XCTAssertTrue((editor.value as? String ?? "").contains("BuildTest"))
    }
    func testInvalidFilenameCannotBeCreated() throws {
        let app = XCUIApplication(); app.launchArguments = ["--ui-testing"]
        app.launchEnvironment["RX_UI_LIBRARY"] = UUID().uuidString
        app.launch()
        XCTAssertTrue(app.buttons["createDatabase"].waitForExistence(timeout: 15)); app.buttons["createDatabase"].tap()
        let field = app.textFields["databaseName"]
        XCTAssertTrue(field.waitForExistence(timeout: 5)); field.tap(); field.typeText("../bad")
        XCTAssertFalse(app.buttons["confirmCreateDatabase"].isEnabled)
    }
}
