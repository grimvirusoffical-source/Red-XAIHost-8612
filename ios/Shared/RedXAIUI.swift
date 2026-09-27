import SwiftUI

public enum RXPalette {
    public static let background = Color(red: 0.027, green: 0.024, blue: 0.027)
    public static let surface = Color(red: 0.075, green: 0.043, blue: 0.075)
    public static let elevated = Color(red: 0.105, green: 0.060, blue: 0.105)
    public static let blood = Color(red: 0.62, green: 0.05, blue: 0.12)
    public static let purple = Color(red: 0.39, green: 0.20, blue: 0.55)
    public static let border = Color.white.opacity(0.09)
}

public struct RXCard<Content: View>: View {
    private let content: Content
    public init(@ViewBuilder content: () -> Content) { self.content = content() }
    public var body: some View {
        content.padding(16)
            .background(RXPalette.surface, in: RoundedRectangle(cornerRadius: 20, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: 20, style: .continuous).stroke(RXPalette.border))
    }
}

public struct RXBrandMark: View {
    public init() {}
    public var body: some View {
        ZStack {
            RoundedRectangle(cornerRadius: 15, style: .continuous)
                .fill(LinearGradient(colors: [RXPalette.blood, RXPalette.purple], startPoint: .topLeading, endPoint: .bottomTrailing))
            Text("X").font(.system(size: 27, weight: .black, design: .rounded)).foregroundStyle(.white)
        }.frame(width: 52, height: 52).shadow(color: RXPalette.blood.opacity(0.25), radius: 18)
    }
}

public struct RXSectionHeader: View {
    let title: String; let detail: String?
    public init(_ title: String, detail: String? = nil) { self.title = title; self.detail = detail }
    public var body: some View {
        HStack(alignment: .firstTextBaseline) {
            Text(title).font(.title3.bold())
            Spacer()
            if let detail { Text(detail).font(.caption).foregroundStyle(.secondary) }
        }
    }
}

public struct RXStatusPill: View {
    let text: String; let active: Bool
    public init(_ text: String, active: Bool = true) { self.text=text; self.active=active }
    public var body: some View {
        HStack(spacing: 6) {
            Circle().fill(active ? Color.green : Color.secondary).frame(width: 7, height: 7)
            Text(text).font(.caption.weight(.semibold))
        }.padding(.horizontal, 10).padding(.vertical, 6)
         .background(Color.white.opacity(0.06), in: Capsule())
    }
}
