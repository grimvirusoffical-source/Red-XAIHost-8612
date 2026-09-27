import Foundation

public enum RXDocumentIOError: Error, Equatable, LocalizedError, Sendable {
    case tooLarge
    case invalidUTF8

    public var errorDescription: String? {
        switch self {
        case .tooLarge: return "Documents must be no larger than 2 MiB. Your existing file has not been replaced."
        case .invalidUTF8: return "This file is not valid UTF-8 text. It was not opened or converted."
        }
    }
}

/// Text-file I/O only. This does not validate the complete Red-XAI language.
public enum RXDocumentCodec {
    public static func decode(_ data: Data) throws -> String {
        guard data.count <= RedXAIValidator.maxBytes else { throw RXDocumentIOError.tooLarge }
        guard let text = String(data: data, encoding: .utf8) else { throw RXDocumentIOError.invalidUTF8 }
        return text
    }

    public static func encode(_ text: String) throws -> Data {
        guard text.utf8.count <= RedXAIValidator.maxBytes else { throw RXDocumentIOError.tooLarge }
        return Data(text.utf8)
    }
}

public enum RXBuildInfo {
    public static var label: String {
        let version = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "Development"
        let build = Bundle.main.object(forInfoDictionaryKey: "CFBundleVersion") as? String ?? "Local"
        return "v\(version) · Build \(build)"
    }
}
