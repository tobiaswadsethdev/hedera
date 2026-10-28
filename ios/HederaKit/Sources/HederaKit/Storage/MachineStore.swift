import Foundation
import Security

/// Everything needed to reach one paired machine. The whole record, token
/// included, lives in the Keychain and nowhere else.
public struct MachineRecord: Codable, Equatable, Identifiable, Sendable {
    public var machineID: String
    public var name: String
    public var hosts: [String]
    public var port: String
    public var fingerprint: String
    public var pairingID: String
    public var token: String
    public var addedAt: Date
    /// This phone's Radix link for the machine, while notifications are on.
    public var pushLinkID: String?

    public var id: String { machineID }

    enum CodingKeys: String, CodingKey {
        case name, hosts, port, fingerprint, token
        case machineID = "machine_id"
        case pairingID = "pairing_id"
        case addedAt = "added_at"
        case pushLinkID = "push_link_id"
    }

    public init(machineID: String, name: String, hosts: [String], port: String, fingerprint: String,
                pairingID: String, token: String, addedAt: Date = Date()) {
        self.machineID = machineID
        self.name = name
        self.hosts = hosts
        self.port = port
        self.fingerprint = fingerprint
        self.pairingID = pairingID
        self.token = token
        self.addedAt = addedAt
    }
}

// Keep the token out of print(), dump(), string interpolation and the debugger summary.
extension MachineRecord: CustomStringConvertible, CustomDebugStringConvertible, CustomReflectable {
    public var description: String { "MachineRecord(\(machineID), \(name), pairing \(pairingID))" }
    public var debugDescription: String { description }
    public var customMirror: Mirror {
        Mirror(self, children: ["machineID": machineID, "name": name, "hosts": hosts, "port": port,
                                "pairingID": pairingID, "token": "<redacted>", "pushLinkID": pushLinkID as Any])
    }
}

public protocol MachineStore: Sendable {
    func loadAll() throws -> [MachineRecord]
    func save(_ record: MachineRecord) throws
    func delete(machineID: String) throws
}

public struct KeychainError: Error, LocalizedError {
    public var status: OSStatus
    public var errorDescription: String? {
        "Keychain error \(status): \(SecCopyErrorMessageString(status, nil) as String? ?? "unknown")"
    }
}

/// One generic-password item per machine. Items are readable after the first
/// unlock (so a notification reply can connect while the phone is locked),
/// never leave this device, and are not synced to iCloud.
public struct KeychainMachineStore: MachineStore {
    private let items: KeychainItems

    public init(service: String = "hedera.machines") {
        items = KeychainItems(service: service)
    }

    public func loadAll() throws -> [MachineRecord] {
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .iso8601
        return try items.all().compactMap { try? decoder.decode(MachineRecord.self, from: $0) }
            .sorted { $0.addedAt < $1.addedAt }
    }

    public func save(_ record: MachineRecord) throws {
        let encoder = JSONEncoder()
        encoder.dateEncodingStrategy = .iso8601
        try items.set(encoder.encode(record), account: record.machineID)
    }

    public func delete(machineID: String) throws {
        try items.delete(account: machineID)
    }
}

/// Generic-password items under one service, keyed by account.
struct KeychainItems {
    let service: String
    /// A Keychain access group to keep the items in; nil is the app's own.
    var accessGroup: String?

    private var base: [String: Any] {
        var q: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrSynchronizable as String: false,
        ]
        if let accessGroup { q[kSecAttrAccessGroup as String] = accessGroup }
        #if os(macOS)
        // Tests on the Mac use the same keychain as the app on iOS.
        q[kSecUseDataProtectionKeychain as String] = true
        #endif
        return q
    }

    func all() throws -> [Data] {
        var q = base
        q[kSecMatchLimit as String] = kSecMatchLimitAll
        q[kSecReturnData as String] = true
        var out: CFTypeRef?
        let status = SecItemCopyMatching(q as CFDictionary, &out)
        if status == errSecItemNotFound { return [] }
        guard status == errSecSuccess, let items = out as? [Data] else { throw KeychainError(status: status) }
        return items
    }

    func get(account: String) throws -> Data? {
        var q = base
        q[kSecAttrAccount as String] = account
        q[kSecReturnData as String] = true
        var out: CFTypeRef?
        let status = SecItemCopyMatching(q as CFDictionary, &out)
        if status == errSecItemNotFound { return nil }
        guard status == errSecSuccess, let data = out as? Data else { throw KeychainError(status: status) }
        return data
    }

    func set(_ data: Data, account: String) throws {
        var q = base
        q[kSecAttrAccount as String] = account
        let update: [String: Any] = [
            kSecValueData as String: data,
            kSecAttrAccessible as String: kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly,
        ]
        var status = SecItemUpdate(q as CFDictionary, update as CFDictionary)
        if status == errSecItemNotFound {
            q.merge(update) { $1 }
            status = SecItemAdd(q as CFDictionary, nil)
        }
        guard status == errSecSuccess else { throw KeychainError(status: status) }
    }

    func delete(account: String) throws {
        var q = base
        q[kSecAttrAccount as String] = account
        let status = SecItemDelete(q as CFDictionary)
        guard status == errSecSuccess || status == errSecItemNotFound else { throw KeychainError(status: status) }
    }
}

/// For tests and previews.
public final class InMemoryMachineStore: MachineStore, @unchecked Sendable {
    private let lock = NSLock()
    private var records: [String: MachineRecord] = [:]

    public init(_ records: [MachineRecord] = []) {
        for r in records { self.records[r.machineID] = r }
    }

    public func loadAll() throws -> [MachineRecord] {
        lock.withLock { records.values.sorted { $0.addedAt < $1.addedAt } }
    }

    public func save(_ record: MachineRecord) throws {
        lock.withLock { records[record.machineID] = record }
    }

    public func delete(machineID: String) throws {
        _ = lock.withLock { records.removeValue(forKey: machineID) }
    }
}
