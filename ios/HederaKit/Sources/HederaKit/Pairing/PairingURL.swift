import Foundation

/// A `hedera://pair` URL from `hedera pair` (protocol/spec.md section 1.3).
public struct PairingURL: Equatable, Sendable {
    public var machineID: String
    public var name: String
    /// Tailscale addresses in order, then the MagicDNS name if there is one.
    public var hosts: [String]
    public var port: String
    public var fingerprint: String
    public var code: String

    public enum ParseError: Error, LocalizedError {
        case notPairingURL
        case unsupportedVersion(String)
        case missingFields

        public var errorDescription: String? {
            switch self {
            case .notPairingURL: "That QR code isn't a Hedera pairing code."
            case let .unsupportedVersion(v): "This pairing code needs a newer app (version \(v))."
            case .missingFields: "The pairing code is incomplete."
            }
        }
    }

    public init(_ raw: String) throws {
        guard let c = URLComponents(string: raw.trimmingCharacters(in: .whitespacesAndNewlines)),
              c.scheme == "hedera", c.host == "pair"
        else { throw ParseError.notPairingURL }
        let items = c.queryItems ?? []
        func first(_ k: String) -> String { items.first { $0.name == k }?.value ?? "" }
        guard first("v") == "1" else { throw ParseError.unsupportedVersion(first("v")) }
        machineID = first("m")
        name = first("n")
        port = first("p")
        fingerprint = first("fp")
        code = first("c")
        hosts = items.filter { $0.name == "a" }.compactMap(\.value)
        if case let d = first("d"), !d.isEmpty { hosts.append(d) }
        guard !port.isEmpty, !fingerprint.isEmpty, !code.isEmpty, !hosts.isEmpty else {
            throw ParseError.missingFields
        }
    }
}

extension String {
    /// Brackets IPv6 literals for use in a URL authority.
    var urlHost: String { contains(":") ? "[\(self)]" : self }
}
