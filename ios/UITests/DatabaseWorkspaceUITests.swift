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
        XCTAssertTrue(field.waitForExistence(timeout: 5))
        field.tap()
        field.typeText("Smoke")
        app.buttons["confirmCreateDatabase"].tap()

        let editor = app.textViews["sourceEditor"]
        XCTAssertTrue(editor.waitForExistence(timeout: 15))
        XCTAssertTrue((editor.value as? String ?? "").contains("{Red-XAI}[1]"))
        let initialRevision = try waitForSavedRevision(in: app)

        app.buttons["databaseOutline"].tap()
        let boxMenu = app.buttons["boxMenu-Profile"]
        XCTAssertTrue(boxMenu.waitForExistence(timeout: 10))
        boxMenu.tap()
        let addPacker = app.buttons["Add Packer"]
        XCTAssertTrue(addPacker.waitForExistence(timeout: 10))
        addPacker.tap()
        let name = app.textFields["structureName"]
        XCTAssertTrue(name.waitForExistence(timeout: 5))
        name.tap()
        name.typeText("BuildTest")
        XCTAssertEqual(name.value as? String, "BuildTest")
        let apply = app.buttons["applyStructure"]
        apply.tap()
        XCTAssertTrue(apply.waitForNonExistence(timeout: 10), "Apply must succeed and dismiss the form. \(app.staticTexts["structureError"].exists ? app.staticTexts["structureError"].label : "No form error reported")")

        // A SwiftUI Button is a single accessibility element. Its nested Text
        // is not a reliable standalone query, and lazy rows may be offscreen.
        let packer = app.buttons["packerRow-BuildTest"]
        for _ in 0..<6 {
            if packer.exists && packer.isHittable { break }
            app.swipeUp()
        }
        XCTAssertTrue(packer.waitForExistence(timeout: 10), "The actual parsed Packer must appear in the outline.")
        XCTAssertEqual(packer.label, "BuildTest")
        XCTAssertTrue(packer.isHittable)
        packer.tap()
        let valueEditor = app.textViews["structureValue"]
        XCTAssertTrue(valueEditor.waitForExistence(timeout: 10))
        XCTAssertEqual(valueEditor.value as? String, "NELL", "Opening the new row must show the parsed value.")
        app.buttons["Cancel"].tap()
        app.navigationBars["Structure"].buttons["Done"].tap()
        XCTAssertTrue(editor.waitForExistence(timeout: 5))
        XCTAssertTrue((editor.value as? String ?? "").contains("BuildTest"))

        let saveButton = app.buttons["saveDatabase"]
        let enabled = XCTNSPredicateExpectation(predicate: NSPredicate(format: "enabled == true"), object: saveButton)
        XCTAssertEqual(XCTWaiter.wait(for: [enabled], timeout: 15), .completed)
        saveButton.tap()
        let savedRevision = try waitForSavedRevision(in: app)
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
        let reopenedRevision = try waitForSavedRevision(in: app)
        XCTAssertGreaterThanOrEqual(reopenedRevision, savedRevision)
        XCTAssertEqual(editor.value as? String, savedText, "Relaunch must load the exact saved source, not a fresh template.")
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

    private func waitForSavedRevision(in app: XCUIApplication) throws -> Int {
        let status = app.staticTexts["databaseSaveStatus"]
        let saved = XCTNSPredicateExpectation(predicate: NSPredicate(format: "exists == true AND label == %@", "Saved"), object: status)
        XCTAssertEqual(XCTWaiter.wait(for: [saved], timeout: 15), .completed)
        let value = try XCTUnwrap(status.value as? String, "Saved state must expose the committed revision.")
        return try XCTUnwrap(Int(value), "The committed revision must be numeric.")
    }
}
