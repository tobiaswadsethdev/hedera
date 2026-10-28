import Foundation
import Observation

public enum PushError: Error, Equatable, LocalizedError {
    /// The app was built without a Radix URL.
    case unavailable
    /// iOS hasn't handed out a device token yet, usually because
    /// notifications aren't allowed.
    case notRegistered
    /// The agent is too old to send notifications.
    case unsupported

    public var errorDescription: String? {
        switch self {
        case .unavailable: "This build of Hedera has no notification service configured."
        case .notRegistered: "Allow notifications for Hedera in Settings first."
        case .unsupported: "This machine's hedera is too old to send notifications. Update it and try again."
        }
    }
}

/// Registers this phone with Radix and connects machines to it
/// (protocol/radix-api.md). Each enabled machine has one link; the link's push
/// token goes to the agent with `push.configure` and is not kept here. Link
/// IDs live in the machine records, which the caller saves.
@MainActor
@Observable
public final class PushManager {
    public private(set) var device: PushDevice?
    public var isAvailable: Bool { radix != nil }

    @ObservationIgnored private let radix: RadixClient?
    @ObservationIgnored private let store: PushDeviceStore

    public init(radixURL: URL?, store: PushDeviceStore = KeychainPushDeviceStore(), session: URLSession? = nil) {
        radix = radixURL.map { RadixClient(baseURL: $0, session: session) }
        self.store = store
        device = try? store.load()
    }

    /// Called with each token iOS hands out, at every launch. Updating also
    /// re-enables a device Radix had disabled after APNs refused an old token.
    /// Returns true when the device had to be registered anew, which drops
    /// every link: the caller should enable its machines again.
    @discardableResult
    public func deviceTokenChanged(_ token: Data, environment: APNsEnvironment) async throws -> Bool {
        guard let radix else { throw PushError.unavailable }
        let hex = token.map { String(format: "%02x", $0) }.joined()
        if var d = device {
            d.apnsToken = hex
            d.environment = environment
            do {
                try await radix.updateDevice(d)
                try store.save(d)
                device = d
                return false
            } catch let e as RadixError where e.isGone {
                // Deleted on Radix; register again below.
            }
        }
        let d = try await radix.registerDevice(apnsToken: hex, environment: environment)
        try store.save(d)
        device = d
        return true
    }

    /// Links `machine` to this phone and hands the agent the push token,
    /// replacing any earlier link. Returns the new link ID.
    public func enable(_ machine: MachineClient, prefs: [String: Bool]? = nil) async throws -> String {
        guard let radix else { throw PushError.unavailable }
        guard let device else { throw PushError.notRegistered }
        guard machine.supportsPush else { throw PushError.unsupported }
        if let old = machine.record.pushLinkID {
            try? await radix.unlink(old, device: device)
        }
        let link = try await radix.link(machineID: machine.id, label: machine.record.name, device: device)
        do {
            try await machine.configurePush(radixURL: radix.baseURL.absoluteString, pushToken: link.pushToken, prefs: prefs)
        } catch {
            try? await radix.unlink(link.linkID, device: device)
            throw error
        }
        return link.linkID
    }

    /// Stops notifications from `machine`. Both halves are best effort: an
    /// agent without the route has no token, and a deleted link's token is
    /// refused by Radix.
    public func disable(_ machine: MachineClient) async {
        try? await machine.removePush()
        if let id = machine.record.pushLinkID, let radix, let device {
            try? await radix.unlink(id, device: device)
        }
    }

    /// For a machine that should notify: links it again if the agent lost the
    /// route or Radix rejected the token. Returns the new link ID, or nil when
    /// nothing needed doing.
    public func repairIfNeeded(_ machine: MachineClient) async throws -> String? {
        guard machine.record.pushLinkID != nil, device != nil, machine.supportsPush else { return nil }
        let status = try await machine.pushStatus()
        if status.configured, status.broken == nil { return nil }
        return try await enable(machine, prefs: status.prefs)
    }

    /// Asks Radix to send this phone a test notification.
    public func sendTest() async throws -> RadixClient.TestResult {
        guard let radix else { throw PushError.unavailable }
        guard let device else { throw PushError.notRegistered }
        return try await radix.sendTest(device: device)
    }

    /// Unregisters this phone, which removes every link.
    public func forgetDevice() async {
        if let device, let radix { try? await radix.deleteDevice(device) }
        try? store.delete()
        device = nil
    }
}
