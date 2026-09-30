import SwiftUI
import UniformTypeIdentifiers
import RedXAICore

extension UTType {
    static let redXAI = UTType(exportedAs: "com.redxai.database", conformingTo: .data)
}
struct RXDocument: FileDocument {
    static var readableContentTypes: [UTType] { [.redXAI, .plainText] }
    var text: String
    init(text: String = RXSerializer.template()) { self.text = text }
    init(configuration: ReadConfiguration) throws {
        guard let data = configuration.file.regularFileContents else { throw CocoaError(.fileReadCorruptFile) }
        text = try RXDocumentCodec.decode(data)
    }
    func fileWrapper(configuration: WriteConfiguration) throws -> FileWrapper {
        FileWrapper(regularFileWithContents: try RXDocumentCodec.encode(text))
    }
}

@main
struct RedXAIDatabaseApp: App {
    @StateObject private var application = DatabaseApplication()
    @StateObject private var preferences = DatabasePreferences()
    var body: some Scene {
        WindowGroup {
            DatabaseLibraryView(application: application, preferences: preferences)
                .preferredColorScheme(preferences.theme.appearance == "light" ? .light : .dark)
                .tint(RXPalette.blood)
                .onOpenURL { url in Task { await application.importFile(url) } }
        }
    }
}
