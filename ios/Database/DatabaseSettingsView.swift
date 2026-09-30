import SwiftUI
import UniformTypeIdentifiers
import RedXAICore

struct DatabaseSettingsView: View {
    @ObservedObject var preferences: DatabasePreferences
    @Environment(\.dismiss) private var dismiss
    @State private var exporting = false, importing = false
    @State private var error: String?
    private var cssType: UTType { UTType(filenameExtension: "css") ?? .plainText }
    var body: some View {
        NavigationStack {
            Form {
                Section("Appearance") {
                    Picker("Theme", selection: $preferences.mode) {
                        Text("Blood Night").tag("dark")
                        Text("Paper").tag("light")
                        if preferences.custom != nil { Text(preferences.custom?.name ?? "Custom").tag("custom") }
                    }
                    HStack {
                        Text("Font size")
                        Slider(value: $preferences.fontSize, in: 12...28, step: 1)
                        Text("\(Int(preferences.fontSize))").monospacedDigit()
                    }
                }
                Section("Editing") {
                    Toggle("Line numbers", isOn: $preferences.lineNumbers)
                    Toggle("Wrap long lines", isOn: $preferences.wrap)
                    Toggle("Autosave after editing", isOn: $preferences.autosave)
                    Text("Autosave waits about a second after typing. Save explicitly before force-closing the app. The last 20 saved revisions are kept locally.")
                        .font(.footnote).foregroundStyle(.secondary)
                }
                Section("Custom syntax colors") {
                    Button("Export current CSS template", systemImage: "square.and.arrow.up") { exporting = true }
                    Button("Import CSS color theme", systemImage: "square.and.arrow.down") { importing = true }
                    Text("Only the exported --rx- hexadecimal color variables are accepted. Scripts, URLs and CSS imports never run. Imported syntax colors must meet a 4.5:1 text-contrast check.")
                        .font(.footnote).foregroundStyle(.secondary)
                }
                Section("Storage & security") {
                    Text("Databases and revision history are stored on this device. Exported copies go wherever you choose in Files. This version does not collect sign-in credentials, mint cloud API keys, or connect to a hosting service.")
                    Text("Local storage is capped at 200 databases and 256 MiB including retained history and Trash. A single document is capped at 2 MiB.")
                        .font(.footnote).foregroundStyle(.secondary)
                }
                Section { NavigationLink("Language & API guide", destination: DatabaseGuide()); Text(RXBuildInfo.label).font(.caption.monospaced()) }
            }
            .navigationTitle("Database Settings")
            .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } } }
            .fileExporter(isPresented: $exporting, document: RXDocument(text: preferences.theme.cssTemplate()), contentType: cssType, defaultFilename: "Red-XAI-Theme.css") { result in
                if case .failure(let issue) = result { error = issue.localizedDescription }
            }
            .fileImporter(isPresented: $importing, allowedContentTypes: [cssType, .plainText, .data]) { result in
                do {
                    let url = try result.get()
                    let access = url.startAccessingSecurityScopedResource(); defer { if access { url.stopAccessingSecurityScopedResource() } }
                    let size = try url.resourceValues(forKeys: [.fileSizeKey]).fileSize ?? 0
                    guard size <= 8192 else { throw RXLanguageError.limit }
                    let data = try Data(contentsOf: url)
                    guard let css = String(data: data, encoding: .utf8) else { throw RXDocumentIOError.invalidUTF8 }
                    let name = String(url.deletingPathExtension().lastPathComponent.prefix(40))
                    let theme = try RXEditorTheme.importingCSS(css, name: name, appearance: preferences.theme.appearance)
                    try preferences.install(theme)
                } catch { self.error = error.localizedDescription }
            }
            .alert("Theme", isPresented: Binding(get: { error != nil }, set: { if !$0 { error = nil } })) {
                Button("OK") { error = nil }
            } message: { Text(error ?? "") }
        }
    }
}

struct DatabaseHistoryView: View {
    @ObservedObject var session: DatabaseSession
    @Environment(\.dismiss) private var dismiss
    @State private var records: [RXDatabaseRecord] = []
    @State private var error: String?
    @State private var restore: RXDatabaseRecord?
    @State private var confirming = false
    var body: some View {
        NavigationStack {
            List {
                Section {
                    Text("Current revision \(session.record.revision)").font(.headline)
                    Text("Restoring first saves any current edits, then creates a new revision from the selected snapshot. It does not erase intervening history.")
                        .font(.footnote).foregroundStyle(.secondary)
                }
                if let error { Text(error).foregroundStyle(.red) }
                if records.isEmpty { ContentUnavailableView("No earlier revisions", systemImage: "clock.arrow.circlepath", description: Text("History appears after your first saved change.")) }
                ForEach(records, id: \.revision) { record in
                    NavigationLink {
                        ScrollView {
                            VStack(alignment: .leading, spacing: 16) {
                                Text("Revision \(record.revision)").font(.title2.bold())
                                Text(record.modifiedAt.formatted()).font(.caption).foregroundStyle(.secondary)
                                Button("Restore this revision") { restore = record; confirming = true }.buttonStyle(.borderedProminent)
                                Text(record.source).font(.system(.body, design: .monospaced)).textSelection(.enabled)
                            }.frame(maxWidth: .infinity, alignment: .leading).padding()
                        }.navigationTitle("Snapshot")
                    } label: {
                        VStack(alignment: .leading, spacing: 5) {
                            Text("Revision \(record.revision) · \(record.operation)")
                            Text(record.modifiedAt.formatted()).font(.caption).foregroundStyle(.secondary)
                        }
                    }
                }
            }
            .navigationTitle("Revision History")
            .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } } }
            .task { do { records = try await session.history() } catch { self.error = error.localizedDescription } }
            .confirmationDialog("Restore this saved snapshot?", isPresented: $confirming, titleVisibility: .visible) {
                Button("Restore") { if let revision = restore?.revision { Task { await session.restore(revision); dismiss() } } }
            }
        }
    }
}

struct DatabaseGuide: View {
    var body: some View {
        List {
            Section("What this release does") {
                Text("Create, import, name, duplicate, edit, export and restore local .Red-XAI databases. The native editor provides syntax colors, line numbers, find/replace, undo/redo, a searchable outline and structured Box/Packer/value editing.")
            }
            Section("Boxes and Packers") {
                code("{Red-XAI}[1]{\n    {Profile}[2]{\n        {Name}[1] = [\"Player\"][1],\n    <[False,2,false]>}\n<[False,1,false,\"keyref:local\"]>}")
                Text("One Red-XAI root must be first, with local ID 1. Boxes contain other Boxes, Packers and GAccsess declarations. Each Packer ends with a comma. Names are case-sensitive; quote names that include spaces.")
            }
            Section("Values") {
                code("27, -1.25, \"text\", True, FALSE, NELL\n[1, \"item\", [2, 3]]\ntable[Name = \"Player\", Audio = [Volume = 0.8]]\nmeta[__type = \"Player\", __version = 1]")
                Text("Booleans and NELL ignore letter case. Numbers are finite 64-bit floating-point values. Use quoted strings for exact large identifiers or financial decimal amounts. Tables preserve entry order; duplicate keys are rejected. Metatables are inert metadata, not executable Lua.")
            }
            Section("Comments") {
                code("/- One-line comment\n/- Inline comment -/\n/-\nMultiline comment\n-\\")
                Text("A bare /- on its own line opens a multiline comment. With text on that line it is a single-line comment unless closed on that same line. This removes the ambiguity between the two original comment examples.")
            }
            Section("IDs and visibility") {
                Text("Box local IDs and Box global IDs each have their own file-wide namespace. Packer local uniqueness is name + ID within its parent Box; global uniqueness is name + ID across this file. Box IDs and Packer IDs are separate namespaces. Empty or False Packer ID brackets mean no ID. The outline can search names or either ID.")
                code("<[GAccsess,ProfileReader,1001,]>\n<[True,12,false]>")
                Text("The closing flags record intended cross-database and intra-file visibility. A GAccsess declaration is a named reference. Neither is an authorization token. Remote permissions must be checked by a server. Root exports contain a public keyref: identifier, never a reusable API secret.")
            }
            Section("Reading order") {
                Text("The parser records a Box's header and closing metadata before exposing its contents. Values are declarative: text and array order are preserved, not reversed character-by-character. Executable right-to-left expressions and cross-file reference evaluation are not implemented in this release.")
            }
            Section("Local quick functions") {
                code("let result = RedXAILanguage.inspect(source)\nresult.find(name: \"Player\")\nresult.find(localID: 2)\nresult.find(globalID: 9)\nresult.findBoxes(name: \"Profile\")")
                Text("RXMutations performs bounded source-preserving edits. RXLibrary handles revision-checked local saves, history, duplicate, rename, Trash and restore. The parser and editing APIs are portable Swift; the local storage implementation in this release supports Apple platforms and Linux.")
            }
            Section("Not a live cloud platform yet") {
                Text("Cloud API keys, hosted databases, shared Accounts.Red-XAI authentication, Google/Apple/Discord login, MFA, remote access tokens, owner administration, billing and multi-language network SDKs are not connected here. Do not use this beta as the authoritative store for real account credentials.")
            }
        }.navigationTitle("Red-XAI Guide").navigationBarTitleDisplayMode(.inline)
    }
    private func code(_ text: String) -> some View { Text(text).font(.system(.caption, design: .monospaced)).textSelection(.enabled) }
}
