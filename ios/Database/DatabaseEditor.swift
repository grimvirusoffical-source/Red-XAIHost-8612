import SwiftUI
import RedXAICore

struct DatabaseEditor: View {
    @ObservedObject var session: DatabaseSession
    @ObservedObject var preferences: DatabasePreferences
    @Environment(\.scenePhase) private var scenePhase
    @State private var outline = false, diagnostics = false, information = false, history = false, settings = false
    @State private var exporting = false, renaming = false, goToLine = false
    @State private var line = "", newName = ""
    private var status: String {
        if session.analyzing { return "Analyzing…" }
        guard let snapshot = session.snapshot else { return "Not validated" }
        let errors = snapshot.diagnostics.filter { $0.severity == .error }.count
        if errors > 0 { return "\(errors) errors" }
        return snapshot.diagnostics.isEmpty ? "Valid structure" : "\(snapshot.diagnostics.count) warnings"
    }
    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 10) {
                Image(systemName: "cylinder.split.1x2.fill").foregroundStyle(RXPalette.blood)
                VStack(alignment: .leading, spacing: 3) {
                    Text(session.record.name).font(.subheadline.weight(.semibold)).lineLimit(1)
                    Text(session.saving ? "Saving…" : (session.isDirty ? "Unsaved changes" : "Saved · revision \(session.record.revision)"))
                        .font(.caption).foregroundStyle(.secondary)
                }
                Spacer()
                Button { diagnostics = true } label: {
                    Label(status, systemImage: session.snapshot?.isValid == true ? "checkmark.circle" : "text.magnifyingglass")
                        .font(.caption).lineLimit(2)
                }.accessibilityIdentifier("validationStatus")
            }.padding(14)
            Divider()
            NativeCodeEditor(text: $session.text, snapshot: session.snapshot, theme: preferences.theme,
                             fontSize: preferences.fontSize, wrap: preferences.wrap, lineNumbers: preferences.lineNumbers, command: session.editorCommand)
            Divider()
            HStack {
                Text("\(session.text.components(separatedBy: "\n").count) lines · \(session.text.utf8.count.formatted()) bytes")
                Spacer()
                Text("UTF-8 · Local")
            }.font(.caption2.monospaced()).foregroundStyle(.secondary).padding(.horizontal, 12).padding(.vertical, 8)
            if session.text.utf8.count > 200_000 {
                Text("Large-file mode: plain text keeps editing responsive. Validate still checks the document.")
                    .font(.caption2).foregroundStyle(.secondary).padding(.horizontal)
            }
        }
        .navigationTitle("Workspace").navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItemGroup(placement: .topBarTrailing) {
                Button("Outline", systemImage: "list.bullet.indent") { session.analyze(); outline = true }.accessibilityIdentifier("databaseOutline")
                Button("Save", systemImage: "square.and.arrow.down") { Task { await session.save() } }.disabled(session.saving).accessibilityIdentifier("saveDatabase")
                Menu {
                    Button("Validate", systemImage: "checkmark.shield") { session.analyze(); diagnostics = true }
                    Button("Find & Replace", systemImage: "magnifyingglass") { session.editorCommand = .init(.find) }
                    Button("Go to Line", systemImage: "number") { goToLine = true }
                    Button("Re-indent", systemImage: "increase.indent") { session.mutate { snapshot, _ in try RXSerializer.indent(snapshot) } }
                    Button("Undo", systemImage: "arrow.uturn.backward") { session.editorCommand = .init(.undo) }
                    Button("Redo", systemImage: "arrow.uturn.forward") { session.editorCommand = .init(.redo) }
                    Divider()
                    Button("Revision History", systemImage: "clock.arrow.circlepath") { history = true }
                    Button("Export .Red-XAI", systemImage: "square.and.arrow.up") { exporting = true }
                    Button("Rename", systemImage: "pencil") { newName = session.record.name; renaming = true }
                    Button("Document Info", systemImage: "info.circle") { information = true }
                    Button("Editor Settings", systemImage: "slider.horizontal.3") { settings = true }
                } label: { Image(systemName: "ellipsis.circle") }.accessibilityLabel("Editor actions")
            }
        }
        .task { session.begin() }
        .onDisappear { Task { await session.save() } }
        .onChange(of: scenePhase) { _, phase in if phase != .active { Task { await session.save() } } }
        .sheet(isPresented: $outline) { DatabaseOutlineView(session: session) }
        .sheet(isPresented: $diagnostics) { DatabaseDiagnosticsView(session: session) }
        .sheet(isPresented: $history) { DatabaseHistoryView(session: session) }
        .sheet(isPresented: $settings) { DatabaseSettingsView(preferences: preferences) }
        .sheet(isPresented: $information) {
            NavigationStack {
                List {
                    LabeledContent("File", value: session.record.name)
                    LabeledContent("Revision", value: "\(session.record.revision)")
                    LabeledContent("Encoding", value: "UTF-8")
                    LabeledContent("Boxes", value: "\(session.snapshot?.boxes.count ?? 0)")
                    LabeledContent("Packers", value: "\(session.snapshot?.packers.count ?? 0)")
                    LabeledContent("Size limit", value: "2 MiB")
                    LabeledContent("Storage", value: "This device")
                    LabeledContent("Build", value: RXBuildInfo.label)
                    Text("Access declarations describe visibility. They do not authenticate a caller or create a remote API. A network backend must enforce authorization.")
                        .font(.footnote).foregroundStyle(.secondary)
                }.navigationTitle("Document Info")
                 .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done") { information = false } } }
            }
        }
        .fileExporter(isPresented: $exporting, document: RXDocument(text: session.text), contentType: .redXAI, defaultFilename: session.record.name) { result in
            if case .failure(let error) = result { session.issue = error.localizedDescription }
        }
        .alert("Rename database", isPresented: $renaming) {
            TextField("Name", text: $newName)
            Button("Cancel", role: .cancel) {}
            Button("Rename") { Task { await session.save(name: newName) } }
        }
        .alert("Go to Line", isPresented: $goToLine) {
            TextField("Line number", text: $line).keyboardType(.numberPad)
            Button("Cancel", role: .cancel) {}
            Button("Go") {
                let target = max(1, Int(line) ?? 1)
                var offset = 0
                let rows = session.text.components(separatedBy: "\n")
                for row in rows.prefix(min(target - 1, max(0, rows.count - 1))) { offset += row.utf16.count + 1 }
                session.go(to: offset)
            }
        }
        .alert("Database", isPresented: Binding(get: { session.issue != nil }, set: { if !$0 { session.issue = nil } })) {
            Button("OK") { session.issue = nil }
        } message: { Text(session.issue ?? "") }
    }
}

struct DatabaseDiagnosticsView: View {
    @ObservedObject var session: DatabaseSession
    @Environment(\.dismiss) private var dismiss
    var body: some View {
        NavigationStack {
            List {
                if session.analyzing { ProgressView("Analyzing document…") }
                if let snapshot = session.snapshot {
                    if snapshot.diagnostics.isEmpty { Label("Syntax and local ID checks passed", systemImage: "checkmark.shield") }
                    ForEach(snapshot.diagnostics) { diagnostic in
                        Button {
                            dismiss(); session.go(to: diagnostic.offset)
                        } label: {
                            VStack(alignment: .leading, spacing: 6) {
                                Label(diagnostic.severity.rawValue.capitalized, systemImage: diagnostic.severity == .error ? "xmark.octagon" : "exclamationmark.triangle")
                                    .foregroundStyle(diagnostic.severity == .error ? .red : .orange).font(.caption.bold())
                                Text(diagnostic.message).foregroundStyle(.primary)
                                Text("Line \(diagnostic.line), column \(diagnostic.column) · Tap to jump").font(.caption).foregroundStyle(.secondary)
                            }.padding(.vertical, 5)
                        }
                    }
                } else if !session.analyzing { ContentUnavailableView("Not validated", systemImage: "doc.text.magnifyingglass") }
                Section {
                    Text("Validation checks this file. Cross-file authorization, remote API keys and account security need a connected backend; this local release does not invent them.")
                        .font(.footnote).foregroundStyle(.secondary)
                }
            }.navigationTitle("Diagnostics")
             .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } } }
        }
    }
}
