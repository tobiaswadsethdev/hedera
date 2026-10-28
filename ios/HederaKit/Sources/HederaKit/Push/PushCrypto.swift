// End-to-end encrypted notification text (protocol/spec.md section 7.4). The
// app makes a key for each push link and gives it to the agent; the
// notification service extension decrypts with it.

import CryptoKit
import Foundation
import Security

public enum PushCrypto {
    public struct Text: Codable, Equatable, Sendable {
        public var title: String
        public var subtitle: String?
        public var body: String
    }

    public enum Failure: Error, Equatable {
        case malformed
        case authentication
    }

    /// A new random 32-byte key.
    public static func newKey() -> Data {
        SymmetricKey(size: .bits256).withUnsafeBytes { Data($0) }
    }

    /// The additional data that binds a ciphertext to its notification.
    static func additionalData(machineID: String, kind: String, sessionID: String?, turn: Int?) -> Data {
        Data("hedera-push-v1\n\(machineID)\n\(kind)\n\(sessionID ?? "")\n\(turn.map(String.init) ?? "")".utf8)
    }

    /// Decrypts the `enc` field of a notification's `hedera` payload.
    public static func open(_ encrypted: String, key: Data, machineID: String, kind: String,
                            sessionID: String?, turn: Int?) throws -> Text {
        guard let raw = Data(base64URL: encrypted), raw.count >= 12 + 16, key.count == 32 else { throw Failure.malformed }
        let box: AES.GCM.SealedBox
        do {
            box = try AES.GCM.SealedBox(combined: raw)
        } catch {
            throw Failure.malformed
        }
        let plain: Data
        do {
            plain = try AES.GCM.open(box, using: SymmetricKey(data: key),
                                     authenticating: additionalData(machineID: machineID, kind: kind, sessionID: sessionID, turn: turn))
        } catch {
            throw Failure.authentication
        }
        guard let text = try? JSONDecoder().decode(Text.self, from: plain) else { throw Failure.malformed }
        return text
    }

    /// Opens a notification's `hedera` payload with the key stored for its
    /// machine. Nil when it isn't encrypted or can't be opened.
    public static func open(payload: [AnyHashable: Any], keys: PushKeyStore) -> Text? {
        guard let h = payload["hedera"] as? [String: Any],
              let enc = h["enc"] as? String,
              let machineID = h["machine_id"] as? String,
              let kind = h["kind"] as? String,
              let key = try? keys.key(machineID: machineID)
        else { return nil }
        return try? open(enc, key: key, machineID: machineID, kind: kind,
                         sessionID: h["session_id"] as? String, turn: (h["turn"] as? NSNumber)?.intValue)
    }
}

extension Data {
    init?(base64URL s: String) {
        var b = s.replacingOccurrences(of: "-", with: "+").replacingOccurrences(of: "_", with: "/")
        b += String(repeating: "=", count: (4 - b.count % 4) % 4)
        self.init(base64Encoded: b)
    }

    var base64URL: String {
        base64EncodedString()
            .replacingOccurrences(of: "+", with: "-")
            .replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "=", with: "")
    }
}

/// Push keys, one per machine. The notification service extension reads
/// them, so they live in a Keychain group shared with it, apart from the
/// machine records: the extension can decrypt notifications but never sees
/// a pairing token.
public protocol PushKeyStore: Sendable {
    func key(machineID: String) throws -> Data?
    func save(_ key: Data, machineID: String) throws
    func delete(machineID: String) throws
}

public struct KeychainPushKeyStore: PushKeyStore {
    private let items: KeychainItems

    /// `accessGroup` is the group shared with the extension; nil uses the
    /// app's own (tests).
    public init(service: String = "hedera.pushkeys", accessGroup: String?) {
        items = KeychainItems(service: service, accessGroup: accessGroup)
    }

    /// The shared group from the `HederaKeychainGroup` Info.plist key, which
    /// the app and the extension both carry.
    public static var sharedGroup: String? {
        (Bundle.main.object(forInfoDictionaryKey: "HederaKeychainGroup") as? String).flatMap { $0.isEmpty ? nil : $0 }
    }

    public func key(machineID: String) throws -> Data? { try items.get(account: machineID) }
    public func save(_ key: Data, machineID: String) throws { try items.set(key, account: machineID) }
    public func delete(machineID: String) throws { try items.delete(account: machineID) }
}

/// For tests and previews.
public final class InMemoryPushKeyStore: PushKeyStore, @unchecked Sendable {
    private let lock = NSLock()
    private var keys: [String: Data] = [:]

    public init() {}

    public func key(machineID: String) throws -> Data? { lock.withLock { keys[machineID] } }
    public func save(_ key: Data, machineID: String) throws { lock.withLock { keys[machineID] = key } }
    public func delete(machineID: String) throws { _ = lock.withLock { keys.removeValue(forKey: machineID) } }
}
