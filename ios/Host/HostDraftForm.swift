import SwiftUI
import RedXAICore

struct HostDraftForm: View {
    let save: (String, RXNodeKind) -> Bool
    @Environment(\.dismiss) private var dismiss
    @State private var name = ""
    @State private var kind = RXNodeKind.windows
    @State private var saveFailed = false

    var body: some View {
        NavigationStack {
            Form {
                Section("Local node plan") {
                    TextField("Name, for example Studio PC", text: $name)
                        .autocorrectionDisabled()
                    Picker("Platform", selection: $kind) {
                        ForEach(RXNodeKind.allCases) { value in Text(value.rawValue).tag(value) }
                    }
                    Text("Use 1–60 characters. Do not enter passwords or access keys.")
                        .font(.caption).foregroundStyle(.secondary)
                    if !name.isEmpty && !RXNodeDraftCodec.validName(name) {
                        Label("Enter a valid name of at most 60 characters.", systemImage: "exclamationmark.triangle")
                            .font(.caption)
                    }
                }
                Section {
                    Text("Saving creates a local setup draft, not a remote connection. Actual device enrollment is not implemented in this beta.")
                }
                if saveFailed {
                    Text("The draft was not saved. Close this sheet to review the storage error, then try again.")
                }
            }
            .navigationTitle("New node draft")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Save") {
                        if save(name, kind) { dismiss() } else { saveFailed = true }
                    }.disabled(!RXNodeDraftCodec.validName(name))
                }
            }
        }
    }
}
