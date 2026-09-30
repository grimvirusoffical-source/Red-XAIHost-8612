import SwiftUI
import UIKit
import RedXAICore

struct RXEditorCommand: Equatable {
    enum Action: Equatable { case none, undo, redo, find, goToOffset(Int), insert(String) }
    let id = UUID()
    let action: Action
    init(_ action: Action) { self.action = action }
}

extension UIColor {
    convenience init(rxHex: String) {
        let n = UInt32(rxHex.dropFirst(), radix: 16) ?? 0xFFFFFF
        self.init(red: CGFloat((n >> 16) & 255) / 255, green: CGFloat((n >> 8) & 255) / 255, blue: CGFloat(n & 255) / 255, alpha: 1)
    }
}

struct NativeCodeEditor: UIViewRepresentable {
    @Binding var text: String
    let snapshot: RXLanguageSnapshot?
    let theme: RXEditorTheme
    let fontSize: Double
    let wrap: Bool
    let lineNumbers: Bool
    let command: RXEditorCommand

    func makeCoordinator() -> Coordinator { Coordinator(self) }
    func makeUIView(context: Context) -> CodeEditorPane {
        let pane = CodeEditorPane()
        pane.editor.delegate = context.coordinator
        pane.editor.text = text
        pane.editor.isFindInteractionEnabled = true
        pane.editor.autocorrectionType = .no
        pane.editor.autocapitalizationType = .none
        pane.editor.smartQuotesType = .no
        pane.editor.smartDashesType = .no
        pane.editor.smartInsertDeleteType = .no
        pane.editor.spellCheckingType = .no
        pane.editor.keyboardDismissMode = .interactive
        pane.editor.accessibilityLabel = "Red-XAI source editor"
        pane.editor.accessibilityIdentifier = "sourceEditor"
        pane.editor.textContainerInset = UIEdgeInsets(top: 12, left: 6, bottom: 48, right: 12)
        pane.editor.alwaysBounceVertical = true
        let toolbar = UIToolbar()
        toolbar.items = [
            UIBarButtonItem(title: "Undo", primaryAction: UIAction { [weak pane] _ in pane?.editor.undoManager?.undo() }),
            UIBarButtonItem(title: "Redo", primaryAction: UIAction { [weak pane] _ in pane?.editor.undoManager?.redo() }),
            UIBarButtonItem(title: "Tab", primaryAction: UIAction { [weak pane] _ in pane?.editor.insertText("    ") }),
            UIBarButtonItem(systemItem: .flexibleSpace),
            UIBarButtonItem(title: "Done", primaryAction: UIAction { [weak pane] _ in pane?.editor.resignFirstResponder() })
        ]
        toolbar.sizeToFit(); pane.editor.inputAccessoryView = toolbar
        context.coordinator.pane = pane
        return pane
    }
    func updateUIView(_ pane: CodeEditorPane, context: Context) {
        let coordinator = context.coordinator
        coordinator.parent = self
        if pane.editor.text != text && pane.editor.markedTextRange == nil {
            coordinator.suppressChanges = true
            if let range = pane.editor.textRange(from: pane.editor.beginningOfDocument, to: pane.editor.endOfDocument) {
                pane.editor.replace(range, withText: text)
            }
            coordinator.suppressChanges = false
        }
        pane.showNumbers = lineNumbers
        pane.wrap = wrap
        pane.setNeedsLayout()
        coordinator.restyle()
        if coordinator.lastCommand != command.id {
            coordinator.lastCommand = command.id
            switch command.action {
            case .none: break
            case .undo: pane.editor.undoManager?.undo()
            case .redo: pane.editor.undoManager?.redo()
            case .find: pane.editor.findInteraction?.presentFindNavigator(showingReplace: true)
            case .insert(let value): pane.editor.insertText(value)
            case .goToOffset(let offset):
                let location = max(0, min(offset, (pane.editor.text as NSString).length))
                pane.editor.selectedRange = NSRange(location: location, length: 0)
                pane.editor.scrollRangeToVisible(pane.editor.selectedRange)
                pane.editor.becomeFirstResponder()
            }
        }
    }
    @MainActor
    final class Coordinator: NSObject, UITextViewDelegate {
        var parent: NativeCodeEditor
        weak var pane: CodeEditorPane?
        var suppressChanges = false
        var lastCommand: UUID?
        private var paintedSource: String?
        private var paintedTheme: RXEditorTheme?
        private var paintedFont: Double?
        private var paintedSnapshot = false
        init(_ parent: NativeCodeEditor) { self.parent = parent }
        func textViewDidChange(_ textView: UITextView) {
            if !suppressChanges && parent.text != textView.text { parent.text = textView.text }
            textView.typingAttributes[.foregroundColor] = UIColor(rxHex: parent.theme.colors["foreground"]!)
            pane?.ruler.updateLines(textView.text)
            pane?.ruler.setNeedsDisplay()
        }
        func scrollViewDidScroll(_ scrollView: UIScrollView) { pane?.ruler.setNeedsDisplay() }
        func textViewDidChangeSelection(_ textView: UITextView) { pane?.ruler.setNeedsDisplay() }
        func textView(_ textView: UITextView, shouldChangeTextIn range: NSRange, replacementText value: String) -> Bool {
            guard let swiftRange = Range(range, in: textView.text) else { return false }
            let bytes = textView.text.utf8.count - textView.text[swiftRange].utf8.count + value.utf8.count
            if bytes > RedXAIValidator.maxBytes {
                UIAccessibility.post(notification: .announcement, argument: "The paste exceeds the 2 MiB document limit.")
                return false
            }
            if value == "\n" {
                let ns = textView.text as NSString
                let lineRange = ns.lineRange(for: NSRange(location: min(range.location, ns.length), length: 0))
                let prefixLength = max(0, range.location - lineRange.location)
                let prefix = ns.substring(with: NSRange(location: lineRange.location, length: prefixLength))
                let indentation = String(prefix.prefix { $0 == " " || $0 == "\t" })
                if !indentation.isEmpty {
                    textView.insertText("\n" + indentation)
                    return false
                }
            }
            return true
        }
        func restyle() {
            guard let pane, pane.editor.markedTextRange == nil else { return }
            let view = pane.editor, source = view.text ?? ""
            let snapshot = parent.snapshot?.source == source ? parent.snapshot : nil
            let colored = snapshot != nil && source.utf8.count <= 200_000
            guard paintedSource != source || paintedTheme != parent.theme || paintedFont != parent.fontSize || paintedSnapshot != colored else { return }
            paintedSource = source; paintedTheme = parent.theme; paintedFont = parent.fontSize; paintedSnapshot = colored
            let colors = parent.theme.colors.mapValues { UIColor(rxHex: $0) }
            let font = UIFontMetrics(forTextStyle: .body).scaledFont(for: .monospacedSystemFont(ofSize: CGFloat(min(28, max(12, parent.fontSize))), weight: .regular))
            view.backgroundColor = colors["background"]; pane.backgroundColor = colors["background"]
            view.textColor = colors["foreground"]; view.tintColor = parent.theme.appearance == "light" ? .systemRed : .systemPink
            view.font = font; view.adjustsFontForContentSizeCategory = true
            view.typingAttributes = [.font: font, .foregroundColor: colors["foreground"]!]
            view.undoManager?.disableUndoRegistration()
            view.textStorage.beginEditing()
            let full = NSRange(location: 0, length: (source as NSString).length)
            view.textStorage.setAttributes([.font: font, .foregroundColor: colors["foreground"]!], range: full)
            if colored, let snapshot {
                let boxOffsets = Set(snapshot.boxes.map { $0.span.offset })
                let packerOffsets = Set(snapshot.packers.map { $0.span.offset })
                var previous: RXToken?
                for token in snapshot.tokens where token.kind != .eof {
                    var key: String
                    switch token.kind {
                    case .number: key = "number"
                    case .boolean: key = "boolean"
                    case .nell: key = "nell"
                    case .string: key = "string"
                    case .comment: key = "comment"
                    case .symbol: key = ["[", "]"].contains(token.text) ? "square" : (["{", "}"].contains(token.text) ? "curly" : "symbol")
                    default: key = "foreground"
                    }
                    if (token.kind == .name || token.kind == .string), let previous, previous.text == "{" {
                        if boxOffsets.contains(previous.span.offset) { key = "box" }
                        if packerOffsets.contains(previous.span.offset) { key = "packer" }
                    }
                    if token.span.end <= full.length { view.textStorage.addAttribute(.foregroundColor, value: colors[key]!, range: token.span.range) }
                    if token.kind != .comment { previous = token }
                }
            }
            view.textStorage.endEditing(); view.undoManager?.enableUndoRegistration()
            pane.ruler.ink = colors["comment"]!; pane.ruler.backgroundColor = colors["background"]!
            pane.ruler.updateLines(source); pane.ruler.setNeedsDisplay()
        }
    }
}

@MainActor
final class CodeEditorPane: UIView {
    let editor = UITextView(usingTextLayoutManager: false)
    let ruler = CodeLineRuler()
    var showNumbers = true
    var wrap = true
    override init(frame: CGRect) { super.init(frame: frame); addSubview(editor); addSubview(ruler); ruler.editor = editor }
    required init?(coder: NSCoder) { fatalError("init(coder:) is not used") }
    override func layoutSubviews() {
        super.layoutSubviews()
        let width: CGFloat = showNumbers ? 44 : 0
        ruler.isHidden = !showNumbers; ruler.frame = CGRect(x: 0, y: 0, width: width, height: bounds.height)
        editor.frame = CGRect(x: width, y: 0, width: max(0, bounds.width - width), height: bounds.height)
        editor.textContainer.widthTracksTextView = wrap
        if !wrap { editor.textContainer.size = CGSize(width: 12_000, height: CGFloat.greatestFiniteMagnitude) }
        ruler.setNeedsDisplay()
    }
}

@MainActor
final class CodeLineRuler: UIView {
    weak var editor: UITextView?
    var ink: UIColor = .secondaryLabel
    private var starts = [0]
    private var currentSource = ""
    func updateLines(_ source: String) {
        guard source != currentSource else { return }
        currentSource = source; starts = [0]
        for (index, value) in source.utf16.enumerated() where value == 10 { starts.append(index + 1) }
    }
    override func draw(_ rect: CGRect) {
        guard let editor else { return }
        let length = (editor.text as NSString).length
        let layout = editor.layoutManager
        let visible = CGRect(x: 0, y: max(0, editor.contentOffset.y - editor.textContainerInset.top), width: editor.bounds.width, height: editor.bounds.height)
        let glyphs = layout.glyphRange(forBoundingRect: visible, in: editor.textContainer)
        let characters = layout.characterRange(forGlyphRange: glyphs, actualGlyphRange: nil)
        var lower = 0, upper = starts.count
        while lower < upper { let mid = (lower + upper) / 2; if starts[mid] < characters.location { lower = mid + 1 } else { upper = mid } }
        let begin = max(0, lower - 1)
        for index in begin..<min(starts.count, begin + 150) {
            let start = starts[index]
            if start > characters.location + characters.length + 1 { break }
            let y: CGFloat
            if start < length {
                let glyph = layout.glyphIndexForCharacter(at: start)
                y = layout.lineFragmentRect(forGlyphAt: glyph, effectiveRange: nil).minY + editor.textContainerInset.top - editor.contentOffset.y
            } else {
                y = editor.caretRect(for: editor.endOfDocument).minY - editor.contentOffset.y
            }
            let text = String(index + 1) as NSString
            let attributes: [NSAttributedString.Key: Any] = [.font: UIFont.monospacedSystemFont(ofSize: 11, weight: .regular), .foregroundColor: ink]
            let size = text.size(withAttributes: attributes)
            text.draw(at: CGPoint(x: max(2, bounds.width - size.width - 6), y: y + 2), withAttributes: attributes)
        }
    }
}
