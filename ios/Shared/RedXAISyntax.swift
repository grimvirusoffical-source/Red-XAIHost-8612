import Foundation
import SwiftUI

public enum RXSyntax {
    public static let box = Color(red: 0.72, green: 0.36, blue: 0.06)
    public static let packer = Color(red: 0.93, green: 0.55, blue: 0.23)
    public static let square = Color(red: 0.90, green: 0.76, blue: 0.36)
    public static let curly = Color(red: 0.64, green: 0.48, blue: 0.00)
    public static let symbol = Color(red: 0.26, green: 0.72, blue: 0.35)
    public static let number = Color(red: 0.29, green: 0.55, blue: 1.00)
    public static let boolean = Color(red: 0.21, green: 0.83, blue: 0.78)
    public static let string = Color(red: 0.70, green: 0.24, blue: 1.00)
    public static let nell = Color(red: 0.55, green: 1.00, blue: 0.21)
    public static let comment = Color.secondary
}

public enum RXDocumentMetrics {
    public static func lineCount(_ source: String) -> Int { max(1, source.split(separator: "\n", omittingEmptySubsequences: false).count) }
    public static func byteCount(_ source: String) -> Int { source.utf8.count }
    public static func rootPresent(_ source: String) -> Bool { source.contains("{Red-XAI}[1]{") }
}
