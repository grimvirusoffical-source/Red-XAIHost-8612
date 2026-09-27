import SwiftUI
import UniformTypeIdentifiers
import RedXAICore

extension UTType {
    static let redXAI = UTType(exportedAs: "com.redxai.database", conformingTo: .data)
}

struct RXDocument: FileDocument {
    static var readableContentTypes: [UTType] { [.redXAI, .plainText] }
    var text: String

    init(text: String = Self.template) { self.text = text }
    init(configuration: ReadConfiguration) throws {
        guard let data = configuration.file.regularFileContents else { throw CocoaError(.fileReadCorruptFile) }
        text = try RXDocumentCodec.decode(data)
    }
    func fileWrapper(configuration: WriteConfiguration) throws -> FileWrapper {
        FileWrapper(regularFileWithContents: try RXDocumentCodec.encode(text))
    }
    static let template = "{Red-XAI}[1]{\n    {PInfo}[232]{\n        {PlayerName}[2] = [\"Player\"][2],\n        {PlayerVerified}[2] = [False][2],\n    <[True,13,false]>}\n<[True,1,false,\"keyref:database-reference\"]>}\n"
}

@main
struct RedXAIDatabaseApp: App {
    var body: some Scene {
        DocumentGroup(newDocument: RXDocument()) { file in
            DatabaseEditor(document: file.$document)
                .preferredColorScheme(.dark)
        }
    }
}
