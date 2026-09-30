import XCTest

@MainActor
final class DatabaseWorkspaceUITests: XCTestCase {
    override func setUpWithError() throws { continueAfterFailure = false }

    func testCreateStructureSaveAndReopen() async throws {
        let app = XCUIApplication()
        app.launchArguments = ["--ui-testing"]
        app.launchEnvironment["RX_UI_LIBRARY"] = UUID().uuidString
        app.launch()
        XCTAssertTrue(app.buttons["createDatabase"].waitForExistence(timeout: 15))
        app.buttons["createDatabase"].tap()
        let field = app.textFields["databaseName"]
        XCTAssertTrue(field.waitForExistence(timeout: 5))
        field.tap()
        field.typeText("Smoke")
        app.buttons["confirmCreateDatabase"].tap()

        let editor = app.textViews["sourceEditor"]
        XCTAssertTrue(editor.waitForExistence(timeout: 15))
        XCTAssertTrue((editor.value as? String ?? "").contains("{Red-XAI}[1]"))
        let initialRevision = try await waitForSavedRevision(in: app)

        app.buttons["databaseOutline"].tap()
        let boxMenu = app.buttons["boxMenu-Profile"]
        XCTAssertTrue(boxMenu.waitForExistence(timeout: 10))
        boxMenu.tap()
        app.buttons["Add Packer"].tap()
        let name = app.textFields["structureName"]
        XCTAssertTrue(name.waitForExistence(timeout: 5))
        name.tap()
        name.typeText("BuildTest")
        app.buttons["applyStructure"].tap()
        XCTAssertTrue(app.staticTexts["BuildTest"].waitForExistence(timeout: 10))
        app.navigationBars["Structure"].buttons["Done"].tap()
        XCTAssertTrue(editor.waitForExistence(timeout: 5))
        XCTAssertTrue((editor.value as? String ?? "").contains("BuildTest"))

        // Autosave can legitimately create more than one revision during editing.
        // Check state plus monotonic revision, not the exact rendered string "2".
        let saveButton = app.buttons["saveDatabase"]
        let enabled = XCTNSPredicateExpectation(predicate: NSPredicate(format: "enabled == true"), object: saveButton)
        await fulfillment(of: [enabled], timeout: 15)
        saveButton.tap()
        let savedRevision = try await waitForSavedRevision(in: app)
        XCTAssertGreaterThan(savedRevision, initialRevision, "The edit must create a durable revision.")
        let savedText = try XCTUnwrap(editor.value as? String)
        XCTAssertTrue(savedText.contains("BuildTest"))

        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "Database editor after saving"
        shot.lifetime = .keepAlways
        add(shot)

        app.terminate()
        app.launch()
        let record = app.staticTexts["Smoke.Red-XAI"]
        XCTAssertTrue(record.waitForExistence(timeout: 15))
        record.tap()
        XCTAssertTrue(editor.waitForExistence(timeout: 15))
        let reopenedRevision = try await waitForSavedRevision(in: app)
        XCTAssertGreaterThanOrEqual(reopenedRevision, savedRevision)
        XCTAssertEqual(editor.value as? String, savedText, "Relaunch must load the actual saved source, not a fresh template.")
    }

    func testInvalidFilenameCannotBeCreated() throws {
        let app = XCUIApplication()
        app.launchArguments = ["--ui-testing"]
        app.launchEnvironment["RX_UI_LIBRARY"] = UUID().uuidString
        app.launch()
        XCTAssertTrue(app.buttons["createDatabase"].waitForExistence(timeout: 15))
        app.buttons["createDatabase"].tap()
        let field = app.textFields["databaseName"]
        XCTAssertTrue(field.waitForExistence(timeout: 5))
        field.tap()
        field.typeText("../bad")
        XCTAssertFalse(app.buttons["confirmCreateDatabase"].isEnabled)
    }

    private func waitForSavedRevision(in app: XCUIApplication) async throws -> Int {
        let status = app.staticTexts["databaseSaveStatus"]
        let saved = XCTNSPredicateExpectation(
            predicate: NSPredicate(format: "exists == true AND label == %@", "Saved"),
            object: status
        )
        await fulfillment(of: [saved], timeout: 15)
        let value = try XCTUnwrap(status.value as? String, "Saved state must expose the committed revision.")
        return try XCTUnwrap(Int(value), "The committed revision must be numeric.")
    }
}
