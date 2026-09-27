import SwiftUI
import RedXAICore

struct HostDashboard: View {
    @StateObject private var store = HostDraftStore()
    @State private var adding = false
    @State private var pendingDeletion: UUID?
    @State private var confirmingDelete = false

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 18) {
                    HStack(spacing: 12) {
                        RXBrandMark()
                        VStack(alignment: .leading, spacing: 4) {
                            Text("Your hosting workspace").font(.title3.bold())
                            Text("Plan locally. Connect in a future update.").font(.caption).foregroundStyle(.secondary)
                        }
                    }
                    RXCard {
                        VStack(alignment: .leading, spacing: 10) {
                            RXStatusPill("Not connected", active: false)
                            Text("Live hosting is not configured").font(.headline)
                            Text("This build saves node setup drafts on this iPhone. It does not enroll machines, run websites, or report live CPU and traffic. No API keys or passwords are requested.")
                                .font(.subheadline).foregroundStyle(.secondary)
                        }.frame(maxWidth: .infinity, alignment: .leading)
                    }
                    RXSectionHeader("Node setup drafts", detail: "\(store.drafts.count) saved locally")
                    if store.drafts.isEmpty {
                        RXCard {
                            ContentUnavailableView("Plan your first node", systemImage: "server.rack",
                                description: Text("Save a name and platform for a Windows PC, Mac, or VPS. Drafts survive closing and reopening the app."))
                        }
                    } else {
                        ForEach(store.drafts) { draft in
                            RXCard {
                                HStack(spacing: 12) {
                                    Image(systemName: "server.rack").foregroundStyle(RXPalette.purple)
                                    VStack(alignment: .leading, spacing: 4) {
                                        Text(draft.name).font(.headline)
                                        Text("\(draft.kind.rawValue) · Local draft only").font(.caption).foregroundStyle(.secondary)
                                    }
                                    Spacer()
                                    Button("Delete draft", systemImage: "trash", role: .destructive) {
                                        pendingDeletion = draft.id
                                        confirmingDelete = true
                                    }
                                    .labelStyle(.iconOnly)
                                    .frame(minWidth: 44, minHeight: 44)
                                    .accessibilityLabel("Delete \(draft.name) draft")
                                }
                            }
                        }
                    }
                    Button("Add setup draft", systemImage: "plus.circle") { adding = true }
                        .buttonStyle(.borderedProminent).tint(RXPalette.blood).controlSize(.large)
                        .disabled(!store.ready || store.drafts.count >= RXNodeDraftCodec.maximumDrafts)
                    Text("Next connection milestone: authenticated enrollment, real health checks, and deployment controls.")
                        .font(.footnote).foregroundStyle(.secondary)
                    Text(RXBuildInfo.label).font(.caption2.monospaced()).foregroundStyle(.secondary)
                        .frame(maxWidth: .infinity)
                }.padding(18)
            }
            .background(RXPalette.background.ignoresSafeArea())
            .navigationTitle("Red-XAI Host")
            .navigationBarTitleDisplayMode(.inline)
            .sheet(isPresented: $adding) {
                HostDraftForm { name, kind in store.add(name: name, kind: kind) }
            }
            .confirmationDialog("Delete this local draft?", isPresented: $confirmingDelete, titleVisibility: .visible) {
                Button("Delete draft", role: .destructive) {
                    if let id = pendingDeletion { store.remove(id: id) }
                    pendingDeletion = nil
                }
            } message: { Text("This only removes the local plan. No server or remote project is affected.") }
            .alert("Draft storage", isPresented: Binding(get: { store.issue != nil }, set: { if !$0 { store.issue = nil } })) {
                Button("OK") { store.issue = nil }
            } message: { Text(store.issue ?? "") }
        }
    }
}
