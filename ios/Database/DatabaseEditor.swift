import SwiftUI
import RedXAICore

struct DatabaseEditor: View {
    @Binding var document: RXDocument
    @State private var diagnostics: [RXDiagnostic]?
    @State private var validating = false
    @State private var showDiagnostics = false
    @State private var showDocumentInfo = false
    @FocusState private var editing: Bool

    private var status: String {
        if validating { return "Checking structure…" }
        guard let diagnostics else { return "Not checked" }
        return diagnostics.isEmpty ? "Basic checks passed" : "\(diagnostics.count) diagnostics"
    }

    var body: some View {
        NavigationStack {
            VStack(spacing: 12) {
                HStack(spacing: 12) {
                    RXBrandMark()
                    VStack(alignment: .leading, spacing: 4) {
                        Text("Database workspace").font(.headline)
                        Text("Local document editor").font(.caption).foregroundStyle(.secondary)
                    }
                    Spacer(minLength: 0)
                }
                .padding(.horizontal, 18)

                HStack {
                    Text(status).font(.subheadline)
                    Spacer()
                    if validating { ProgressView() }
                    Button("Validate", systemImage: "checkmark.shield") { validate() }
                        .buttonStyle(.borderedProminent)
                        .tint(RXPalette.blood)
                        .disabled(validating)
                }
                .padding(.horizontal, 18)

                TextEditor(text: $document.text)
                    .font(.system(.body, design: .monospaced))
                    .autocorrectionDisabled()
                    .textInputAutocapitalization(.never)
                    .scrollContentBackground(.hidden)
                    .focused($editing)
                    .padding(12)
                    .background(RXPalette.surface, in: RoundedRectangle(cornerRadius: 20))
                    .overlay(RoundedRectangle(cornerRadius: 20).stroke(RXPalette.border))
                    .padding(.horizontal, 12)
                    .accessibilityLabel("Red-XAI source editor")
                    .onChange(of: document.text) { _, _ in diagnostics = nil }

                VStack(spacing: 4) {
                    Text("\(document.text.split(separator: "\n", omittingEmptySubsequences: false).count) lines · \(document.text.utf8.count) bytes · UTF-8")
                    Text(RXBuildInfo.label)
                }
                .font(.caption2.monospaced())
                .foregroundStyle(.secondary)
                .padding(.bottom, 8)
            }
            .background(RXPalette.background.ignoresSafeArea())
            .navigationTitle("Red-XAI Database")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItemGroup(placement: .topBarTrailing) {
                    Button("Document info", systemImage: "info.circle") { showDocumentInfo = true }
                    Button("Diagnostics", systemImage: "list.bullet.rectangle") { showDiagnostics = true }
                }
                ToolbarItemGroup(placement: .keyboard) {
                    Spacer()
                    Button("Done") { editing = false }
                }
            }
            .sheet(isPresented: $showDiagnostics) { diagnosticsView }
            .sheet(isPresented: $showDocumentInfo) { documentInfoView }
        }
    }

    private func validate() {
        guard !validating else { return }
        validating = true
        let snapshot = document.text
        Task {
            let result = await Task.detached(priority: .userInitiated) {
                RedXAIValidator.validate(snapshot)
            }.value
            if document.text == snapshot { diagnostics = result.diagnostics }
            validating = false
            showDiagnostics = true
        }
    }

    private var documentInfoView: some View {
        NavigationStack {
            List {
                LabeledContent("Format", value: ".Red-XAI / UTF-8")
                LabeledContent("Lines", value: "\(document.text.split(separator: "\n", omittingEmptySubsequences: false).count)")
                LabeledContent("Size", value: "\(document.text.utf8.count) bytes")
                LabeledContent("Limit", value: "\(RedXAIValidator.maxBytes) bytes")
                Section("Current validator") {
                    Text("Checks the required root, document-size limit, strings, braces, brackets, and basic packer comma structure. Full language semantics are still under development.")
                        .foregroundStyle(.secondary)
                }
                Section("Build") { Text(RXBuildInfo.label).font(.system(.body, design: .monospaced)) }
            }
            .navigationTitle("Document info")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done") { showDocumentInfo = false } } }
        }
    }

    private var diagnosticsView: some View {
        NavigationStack {
            List {
                Section {
                    Text("These are basic structural checks, not full Red-XAI type, ID, or access-control validation. Use synthetic data while testing.")
                        .font(.subheadline).foregroundStyle(.secondary)
                }
                if let diagnostics {
                    if diagnostics.isEmpty {
                        Label("Basic checks passed", systemImage: "checkmark.circle")
                    } else {
                        ForEach(diagnostics) { diagnostic in
                            VStack(alignment: .leading, spacing: 6) {
                                Label(diagnostic.severity.rawValue.capitalized,
                                      systemImage: diagnostic.severity == .error ? "xmark.octagon" : "exclamationmark.triangle")
                                    .font(.caption.bold())
                                Text(diagnostic.message)
                                Text("Line \(diagnostic.line)").font(.caption.monospaced()).foregroundStyle(.secondary)
                            }.padding(.vertical, 4)
                        }
                    }
                } else {
                    ContentUnavailableView("Not checked", systemImage: "doc.text.magnifyingglass",
                                           description: Text("Return to the editor and tap Validate. Editing the document clears old results."))
                }
            }
            .navigationTitle("Diagnostics")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done") { showDiagnostics = false } } }
        }
    }
}
