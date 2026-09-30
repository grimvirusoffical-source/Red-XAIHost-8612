import Foundation
import SwiftUI
import RedXAICore

@MainActor
final class DatabasePreferences: ObservableObject {
    @Published var mode: String { didSet { defaults.set(mode, forKey: "rx.editor.mode") } }
    @Published var fontSize: Double { didSet { defaults.set(fontSize, forKey: "rx.editor.font") } }
    @Published var wrap: Bool { didSet { defaults.set(wrap, forKey: "rx.editor.wrap") } }
    @Published var autosave: Bool { didSet { defaults.set(autosave, forKey: "rx.editor.autosave") } }
    @Published var lineNumbers: Bool { didSet { defaults.set(lineNumbers, forKey: "rx.editor.lines") } }
    @Published private(set) var custom: RXEditorTheme?
    private let defaults = UserDefaults.standard
    var theme: RXEditorTheme { mode == "light" ? .light : (mode == "custom" ? custom ?? .dark : .dark) }
    init() {
        mode = UserDefaults.standard.string(forKey: "rx.editor.mode") ?? "dark"
        let size = UserDefaults.standard.double(forKey: "rx.editor.font")
        fontSize = size == 0 ? 16 : min(28, max(12, size))
        wrap = UserDefaults.standard.object(forKey: "rx.editor.wrap") as? Bool ?? true
        autosave = UserDefaults.standard.object(forKey: "rx.editor.autosave") as? Bool ?? true
        lineNumbers = UserDefaults.standard.object(forKey: "rx.editor.lines") as? Bool ?? true
        if let data = UserDefaults.standard.data(forKey: "rx.editor.custom"), data.count <= 8192,
           let theme = try? JSONDecoder().decode(RXEditorTheme.self, from: data), (try? theme.validate()) != nil { custom = theme }
    }
    func install(_ theme: RXEditorTheme) throws {
        try theme.validate()
        let data = try JSONEncoder().encode(theme)
        defaults.set(data, forKey: "rx.editor.custom"); custom = theme; mode = "custom"
    }
}
