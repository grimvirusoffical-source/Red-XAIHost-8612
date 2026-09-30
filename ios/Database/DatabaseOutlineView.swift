import SwiftUI
import RedXAICore

struct RXStructureRequest: Identifiable {
    enum Kind: Equatable { case packer, box, grant, value, access }
    let id = UUID()
    var kind: Kind
    var targetID: String
    var packer: RXPacker?
    var box: RXBox?
}

struct DatabaseOutlineView: View {
    @ObservedObject var session: DatabaseSession
    @Environment(\.dismiss) private var dismiss
    @State private var search = "", scope = "Name"
    @State private var request: RXStructureRequest?
    @State private var deleting: String?
    @State private var confirmDelete = false
    private func matches(name: String, local: Int?, global: Int?) -> Bool {
        if search.isEmpty { return true }
        if scope == "Local ID" { return Int(search) != nil && local == Int(search) }
        if scope == "Global ID" { return Int(search) != nil && global == Int(search) }
        return name.localizedCaseInsensitiveContains(search)
    }
    var body: some View {
        NavigationStack {
            List {
                Picker("Find by", selection: $scope) { ForEach(["Name", "Local ID", "Global ID"], id: \.self) { Text($0) } }.pickerStyle(.segmented)
                if let snapshot = session.snapshot {
                    if !snapshot.isValid { Label("Resolve errors before structured editing.", systemImage: "exclamationmark.triangle").foregroundStyle(.orange) }
                    Section("Boxes") {
                        ForEach(snapshot.boxes.filter { matches(name: $0.name, local: $0.localID, global: $0.globalID) }) { box in
                            VStack(alignment: .leading, spacing: 8) {
                                HStack {
                                    Label(box.name, systemImage: "shippingbox.fill").font(.headline)
                                    Spacer()
                                    Menu {
                                        Button("Add Packer") { request = .init(kind: .packer, targetID: box.id) }
                                        Button("Add nested Box") { request = .init(kind: .box, targetID: box.id) }
                                        Button("Add GAccsess declaration") { request = .init(kind: .grant, targetID: box.id) }
                                        Button("Edit visibility") { request = .init(kind: .access, targetID: box.id, box: box) }
                                        Button("Jump to source") { dismiss(); session.go(to: box.span.offset) }
                                        if box.name != "Red-XAI" {
                                            Button("Delete Box", role: .destructive) { deleting = box.id; confirmDelete = true }
                                        }
                                    } label: { Image(systemName: "ellipsis.circle").frame(minWidth: 44, minHeight: 44) }
                                    .disabled(!session.canEditStructure)
                                    .accessibilityIdentifier("boxMenu-\(box.name)")
                                }
                                Text(box.path).font(.caption.monospaced()).foregroundStyle(.secondary)
                                Text("Local \(box.localID.map(String.init) ?? "none") · Global \(box.globalID.map(String.init) ?? "none") · Line \(box.span.line)")
                                    .font(.caption).foregroundStyle(.secondary)
                            }.padding(.vertical, 4)
                        }
                    }
                    Section("Packers") {
                        ForEach(snapshot.packers.filter { matches(name: $0.name, local: $0.localID, global: $0.globalID) }) { packer in
                            Button { request = .init(kind: .value, targetID: packer.id, packer: packer) } label: {
                                VStack(alignment: .leading, spacing: 6) {
                                    HStack { Text(packer.name).font(.headline); Spacer(); Text(packer.value.typeName).font(.caption).foregroundStyle(.secondary) }
                                    Text((try? RXSerializer.value(packer.value)) ?? "Value").font(.caption.monospaced()).lineLimit(2)
                                    Text("Local \(packer.localID.map(String.init) ?? "none") · Global \(packer.globalID.map(String.init) ?? "none") · Line \(packer.line)").font(.caption2).foregroundStyle(.secondary)
                                }.foregroundStyle(.primary).padding(.vertical, 5)
                            }.disabled(!session.canEditStructure)
                             .contextMenu {
                                 Button("Jump to source") { dismiss(); session.go(to: packer.span.offset) }
                                 Button("Delete Packer", role: .destructive) { deleting = packer.id; confirmDelete = true }
                             }
                        }
                    }
                } else { ProgressView("Updating outline…") }
            }
            .navigationTitle("Structure").searchable(text: $search, prompt: "Quick Find")
            .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } } }
            .sheet(item: $request) { item in StructuredEditForm(session: session, request: item) }
            .confirmationDialog("Delete the selected item?", isPresented: $confirmDelete, titleVisibility: .visible) {
                Button("Delete", role: .destructive) { if let id = deleting { session.mutate { try RXMutations.remove(id, snapshot: $0, currentSource: $1) } } }
            } message: { Text("This changes the local document. Saved revisions remain available in History.") }
        }
    }
}

struct StructuredEditForm: View {
    @ObservedObject var session: DatabaseSession
    let request: RXStructureRequest
    @Environment(\.dismiss) private var dismiss
    @State private var name = "", local = "", global = "", rawValue = "NELL"
    @State private var error: String?
    @State private var cross = false, intra = false, keyReference = "keyref:local"
    var body: some View {
        NavigationStack {
            Form {
                if request.kind == .access {
                    Section("Visibility metadata") {
                        Toggle("Cross-database visibility", isOn: $cross)
                        Toggle("Intra-file visibility", isOn: $intra)
                        TextField("Global ID, or empty", text: $global).keyboardType(.numberPad)
                        if request.box?.name == "Red-XAI" { TextField("Public keyref: identifier", text: $keyReference).textInputAutocapitalization(.never) }
                        Text("These flags do not generate API credentials or bypass server authorization.").font(.footnote).foregroundStyle(.secondary)
                    }
                } else {
                    if request.kind != .value {
                        Section("Name and IDs") {
                            TextField("Name", text: $name).autocorrectionDisabled().accessibilityIdentifier("structureName")
                            TextField(request.kind == .grant ? "Access declaration ID" : "Local ID (optional)", text: $local).keyboardType(.numberPad)
                            if request.kind != .grant { TextField("Global ID (optional)", text: $global).keyboardType(.numberPad) }
                        }
                    }
                    if request.kind == .packer || request.kind == .value {
                        Section("Value") {
                            TextEditor(text: $rawValue).font(.system(.body, design: .monospaced)).frame(minHeight: 140).autocorrectionDisabled().textInputAutocapitalization(.never).accessibilityIdentifier("structureValue")
                            Menu("Insert value template") {
                                Button("String") { rawValue = "\"Text\"" }
                                Button("Number") { rawValue = "0" }
                                Button("Boolean") { rawValue = "True" }
                                Button("NELL") { rawValue = "NELL" }
                                Button("Array") { rawValue = "[1, \"Text\", NELL]" }
                                Button("Table") { rawValue = "table[Name = \"Player\", Enabled = True]" }
                                Button("Metatable") { rawValue = "meta[__type = \"Player\", __version = 1]" }
                            }
                            Text("Values use Red-XAI syntax. Nested arrays and named tables are supported; metatables are data, never executable code.")
                                .font(.footnote).foregroundStyle(.secondary)
                        }
                    }
                }
                if let error { Text(error).foregroundStyle(.red) }
            }
            .navigationTitle(request.kind == .value ? "Edit Packer value" : (request.kind == .access ? "Box visibility" : "Add structure"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) { Button("Apply") { apply() }.accessibilityIdentifier("applyStructure") }
            }
            .onAppear {
                if let packer = request.packer { name = packer.name; rawValue = (try? RXSerializer.value(packer.value)) ?? "NELL" }
                if let box = request.box {
                    cross = box.access?.crossDatabase ?? false; intra = box.access?.intraFile ?? false
                    global = box.globalID.map(String.init) ?? ""; keyReference = box.access?.keyReference ?? "keyref:local"
                }
            }
        }
    }
    private func id(_ text: String) throws -> Int? {
        let raw = text.trimmingCharacters(in: .whitespaces)
        if raw.isEmpty || raw.lowercased() == "false" { return nil }
        guard raw.allSatisfy({ $0.isASCII && $0.isNumber }), let value = Int(raw), value > 0 else { throw RXLanguageError.invalid("IDs must be positive whole numbers.") }
        return value
    }
    private func apply() {
        do {
            guard let snapshot = session.snapshot, session.canEditStructure else { throw RXLanguageError.staleSnapshot }
            let source: String
            switch request.kind {
            case .value:
                source = try RXMutations.setValue(RedXAILanguage.parseValue(rawValue), packerID: request.targetID, snapshot: snapshot, currentSource: session.text)
            case .packer:
                source = try RXMutations.addPacker(name: name, localID: id(local), globalID: id(global), value: RedXAILanguage.parseValue(rawValue), boxID: request.targetID, snapshot: snapshot, currentSource: session.text)
            case .box:
                source = try RXMutations.addBox(name: name, localID: id(local), globalID: id(global), boxID: request.targetID, snapshot: snapshot, currentSource: session.text)
            case .grant:
                guard let token = try id(local) else { throw RXLanguageError.invalid("An access declaration ID is required.") }
                source = try RXMutations.addAccess(name: name, tokenID: token, boxID: request.targetID, snapshot: snapshot, currentSource: session.text)
            case .access:
                source = try RXMutations.setBoxAccess(boxID: request.targetID, crossDatabase: cross, globalID: id(global), intraFile: intra, keyReference: keyReference, snapshot: snapshot, currentSource: session.text)
            }
            session.text = source; session.analyze(); dismiss()
        } catch { self.error = error.localizedDescription }
    }
}
