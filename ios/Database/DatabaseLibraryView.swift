import SwiftUI
import UniformTypeIdentifiers
import RedXAICore

struct DatabaseLibraryView: View {
    @ObservedObject var application: DatabaseApplication
    @ObservedObject var preferences: DatabasePreferences
    @State private var search = ""
    @State private var path: [UUID] = []
    @State private var creating = false
    @State private var importing = false
    @State private var settings = false
    @State private var trash = false
    @State private var pendingTrash: UUID?
    @State private var confirmingTrash = false
    private var filtered: [RXDatabaseRecord] {
        application.records.filter { search.isEmpty || $0.name.localizedCaseInsensitiveContains(search) }
    }
    var body: some View {
        NavigationStack(path: $path) {
            List {
                Section {
                    HStack(spacing: 14) {
                        RXBrandMark()
                        VStack(alignment: .leading, spacing: 4) {
                            Text("Your databases").font(.title2.bold())
                            Text("Local files. Real structure. Your data.").font(.subheadline).foregroundStyle(.secondary)
                        }
                    }.padding(.vertical, 10)
                    HStack {
                        Label("\(application.records.count) databases", systemImage: "externaldrive")
                        Spacer()
                        Label("On this device", systemImage: "iphone")
                    }.font(.caption).foregroundStyle(.secondary)
                }.listRowBackground(Color.clear)
                if filtered.isEmpty {
                    ContentUnavailableView(search.isEmpty ? "Create your first database" : "No matching databases",
                        systemImage: "cylinder.split.1x2", description: Text(search.isEmpty ? "Create a .Red-XAI file or import a copy from Files. Existing documents from earlier versions remain in Files." : "Try a different name."))
                        .listRowBackground(Color.clear)
                }
                Section("My Databases") {
                    ForEach(filtered) { record in
                        NavigationLink(value: record.id) {
                            HStack(spacing: 12) {
                                Image(systemName: "cylinder.split.1x2.fill").foregroundStyle(RXPalette.blood)
                                VStack(alignment: .leading, spacing: 5) {
                                    Text(record.name).font(.headline).lineLimit(2)
                                    Text("Revision \(record.revision) · \(record.source.utf8.count.formatted()) bytes").font(.caption).foregroundStyle(.secondary)
                                    Text(record.modifiedAt, style: .relative).font(.caption2).foregroundStyle(.secondary)
                                }
                            }.padding(.vertical, 5)
                        }
                        .contextMenu {
                            Button("Duplicate", systemImage: "doc.on.doc") { Task { await application.duplicate(record.id) } }
                            Button("Move to Trash", systemImage: "trash", role: .destructive) { pendingTrash = record.id; confirmingTrash = true }
                        }
                    }
                }
                Section {
                    NavigationLink("Language guide & release scope", destination: DatabaseGuide())
                    Button("Recently deleted", systemImage: "trash") { trash = true }
                    Text(RXBuildInfo.label).font(.caption.monospaced()).foregroundStyle(.secondary)
                }
            }
            .navigationTitle("Red-XAI Database")
            .searchable(text: $search, prompt: "Find a database")
            .toolbar {
                ToolbarItem(placement: .topBarLeading) { Button("Settings", systemImage: "gearshape") { settings = true } }
                ToolbarItemGroup(placement: .topBarTrailing) {
                    Button("Import", systemImage: "square.and.arrow.down") { importing = true }.accessibilityIdentifier("importDatabase")
                    Button("Create Database", systemImage: "plus") { creating = true }.accessibilityIdentifier("createDatabase")
                }
            }
            .navigationDestination(for: UUID.self) { id in DatabaseLoader(id: id, application: application, preferences: preferences) }
            .task { await application.refresh() }
            .refreshable { await application.refresh() }
            .onChange(of: path) { _, new in if new.isEmpty { Task { await application.refresh() } } }
            .sheet(isPresented: $creating) { NewDatabaseForm(application: application) { id in path.append(id) } }
            .sheet(isPresented: $settings) { DatabaseSettingsView(preferences: preferences) }
            .sheet(isPresented: $trash) { DatabaseTrashView(application: application) }
            .fileImporter(isPresented: $importing, allowedContentTypes: [.redXAI, .plainText, .data]) { result in
                if case .success(let url) = result { Task { await application.importFile(url) } }
                if case .failure(let error) = result { application.issue = error.localizedDescription }
            }
            .confirmationDialog("Move this database to Trash?", isPresented: $confirmingTrash, titleVisibility: .visible) {
                Button("Move to Trash", role: .destructive) { if let id = pendingTrash { Task { await application.trash(id) } } }
            } message: { Text("You can restore it from Recently deleted. Export important files before testing beta software.") }
            .alert("Database library", isPresented: Binding(get: { application.issue != nil }, set: { if !$0 { application.issue = nil } })) {
                Button("OK") { application.issue = nil }
            } message: { Text(application.issue ?? "") }
        }
    }
}

struct DatabaseLoader: View {
    let id: UUID
    @ObservedObject var application: DatabaseApplication
    @ObservedObject var preferences: DatabasePreferences
    @State private var session: DatabaseSession?
    @State private var error: String?
    var body: some View {
        Group {
            if let session { DatabaseEditor(session: session, preferences: preferences) }
            else if let error { ContentUnavailableView("Unable to open database", systemImage: "exclamationmark.triangle", description: Text(error)) }
            else { ProgressView("Opening database…") }
        }.task(id: id) {
            do { let record = try await application.library.read(id); session = DatabaseSession(record: record, library: application.library, preferences: preferences) }
            catch { self.error = error.localizedDescription }
        }
    }
}

struct NewDatabaseForm: View {
    @ObservedObject var application: DatabaseApplication
    let created: (UUID) -> Void
    @Environment(\.dismiss) private var dismiss
    @State private var name = ""
    @State private var sample = true
    @State private var busy = false
    var body: some View {
        NavigationStack {
            Form {
                Section("Database name") {
                    TextField("Accounts", text: $name).textInputAutocapitalization(.never).autocorrectionDisabled().accessibilityIdentifier("databaseName")
                    Text((try? RXLibrary.normalizedName(name)) ?? "The .Red-XAI extension is added automatically.")
                        .font(.caption.monospaced()).foregroundStyle(.secondary)
                }
                Section { Toggle("Include example data", isOn: $sample) }
                Section {
                    Text("This creates a local database. Calling it Accounts does not create a live sign-in service. Keep real passwords and API secrets out of example documents.")
                        .font(.footnote).foregroundStyle(.secondary)
                }
                if let issue = application.issue { Text(issue).foregroundStyle(.red) }
            }
            .navigationTitle("New Database").navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Create") {
                        busy = true
                        let source = sample ? RXSerializer.template() : "{Red-XAI}[1]{\n<[False,1,false,\"keyref:local\"]>}\n"
                        Task { if let id = await application.create(name: name, source: source) { dismiss(); created(id) }; busy = false }
                    }.disabled((try? RXLibrary.normalizedName(name)) == nil || busy).accessibilityIdentifier("confirmCreateDatabase")
                }
            }
        }
    }
}

struct DatabaseTrashView: View {
    @ObservedObject var application: DatabaseApplication
    @Environment(\.dismiss) private var dismiss
    @State private var deleting: UUID?
    @State private var confirm = false
    var body: some View {
        NavigationStack {
            List {
                if application.discarded.isEmpty { ContentUnavailableView("Trash is empty", systemImage: "trash") }
                ForEach(application.discarded) { record in
                    VStack(alignment: .leading, spacing: 8) {
                        Text(record.name).font(.headline)
                        HStack {
                            Button("Restore") { Task { await application.recover(record.id) } }
                            Spacer()
                            Button("Delete permanently", role: .destructive) { deleting = record.id; confirm = true }
                        }.buttonStyle(.bordered)
                    }.padding(.vertical, 5)
                }
            }.navigationTitle("Recently deleted")
             .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } } }
             .confirmationDialog("Permanently delete this database and its local revisions?", isPresented: $confirm, titleVisibility: .visible) {
                 Button("Delete permanently", role: .destructive) { if let id = deleting { Task { await application.deletePermanently(id) } } }
             } message: { Text("This cannot be undone. Export a backup first.") }
        }
    }
}
