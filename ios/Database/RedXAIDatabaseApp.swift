import SwiftUI
import UniformTypeIdentifiers
import RedXAICore

extension UTType { static let redXAI = UTType(exportedAs:"com.redxai.database",conformingTo:.data) }

struct RXDocument: FileDocument {
    static var readableContentTypes:[UTType]{[.redXAI,.plainText]}
    var text:String
    init(text:String=Self.template){self.text=text}
    init(configuration:ReadConfiguration)throws{
        guard let data=configuration.file.regularFileContents else{throw CocoaError(.fileReadCorruptFile)}
        guard data.count<=RedXAIValidator.maxBytes else{throw CocoaError(.fileReadTooLarge)}
        text=String(decoding:data,as:UTF8.self)
    }
    func fileWrapper(configuration:WriteConfiguration)throws->FileWrapper{FileWrapper(regularFileWithContents:Data(text.utf8))}
    static let template="{Red-XAI}[1]{\n    {PInfo}[232]{\n        {PlayerName}[2] = [\"Player\"][2],\n        {PlayerVerified}[2] = [False][2],\n    <[True,13,false]>}\n<[True,1,false,\"keyref:database-reference\"]>}\n"
}

@main struct RedXAIDatabaseApp:App{
    var body:some Scene{DocumentGroup(newDocument:RXDocument()){file in DatabaseEditor(document:file.$document).preferredColorScheme(.dark)}}
}

struct DatabaseEditor:View{
    @Binding var document:RXDocument
    @State private var diagnostics:[RXDiagnostic]=[]
    @State private var show=false
    var body:some View{
        NavigationStack{
            VStack(spacing:0){
                HStack{Button("Validate"){diagnostics=RedXAIValidator.validate(document.text).diagnostics;show=true}.buttonStyle(.borderedProminent).tint(RedXAITheme.blood);Spacer();Text(diagnostics.isEmpty ? "Ready":"\(diagnostics.count) diagnostics").foregroundStyle(.secondary)}
                    .padding()
                Divider()
                TextEditor(text:$document.text).font(.system(.body,design:.monospaced)).autocorrectionDisabled().textInputAutocapitalization(.never).padding(8)
            }.background(.black).navigationTitle("Red-XAI Database")
             .sheet(isPresented:$show){NavigationStack{List(diagnostics){d in VStack(alignment:.leading){Text(d.severity.rawValue.uppercased()).font(.caption.bold()).foregroundStyle(d.severity == .error ? .red:.orange);Text("Line \(d.line): \(d.message)")}}.navigationTitle("Validation")}}
        }
    }
}
