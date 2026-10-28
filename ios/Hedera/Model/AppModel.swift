import Foundation
import HederaKit
import Observation
import UIKit
import UserNotifications

/// The paired machines, one `MachineClient` (and one WebSocket) each.
@MainActor
@Observable
final class AppModel {
    private(set) var machines: [MachineClient] = []
    var loadError: String?
    let push: PushManager
    /// Why notifications can't be set up, when they can't.
    private(set) var pushError: String?
    /// The session on screen, whose notifications aren't shown.
    var visibleSession: (machineID: String, sessionID: String)?
    /// Where tapping a notification asked to go; the machine list consumes it.
    var pendingRoute: [Route]?

    @ObservationIgnored private let store: MachineStore
    @ObservationIgnored private let transport: TransportFactory
    @ObservationIgnored private var active = false
    /// Machines to link again once online, because Radix forgot this phone.
    @ObservationIgnored private var needsRelink: Set<String> = []
    @ObservationIgnored private var tokenWaiters: [CheckedContinuation<Void, Never>] = []

    static let clientInfo = ClientInfo(
        name: "Hedera-iOS",
        version: Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "0",
        platform: "ios")

    /// From `HEDERA_RADIX_HOST` in Config/Hedera.xcconfig; nil disables notifications.
    static let radixURL: URL? = (Bundle.main.object(forInfoDictionaryKey: "HederaRadixHost") as? String)
        .flatMap { $0.isEmpty ? nil : URL(string: "https://\($0)") }

    static var apnsEnvironment: APNsEnvironment {
        #if DEBUG
        .sandbox
        #else
        .production
        #endif
    }

    init(store: MachineStore = KeychainMachineStore(), transport: @escaping TransportFactory = NetworkTransport.factory,
         push: PushManager = PushManager(radixURL: AppModel.radixURL)) {
        self.store = store
        self.transport = transport
        self.push = push
        do {
            machines = try store.loadAll().map(makeClient)
        } catch {
            loadError = error.localizedDescription
        }
    }

    private func makeClient(_ record: MachineRecord) -> MachineClient {
        let client = MachineClient(record: record, clientInfo: Self.clientInfo, transport: transport)
        client.onOnline = { [weak self, weak client] in
            guard let self, let client else { return }
            Task { await self.repairPush(client) }
        }
        return client
    }

    func machine(_ id: String) -> MachineClient? {
        machines.first { $0.id == id }
    }

    /// Connects every machine; called when the app becomes active.
    func resumeAll() {
        active = true
        for m in machines { m.resume() }
    }

    /// Disconnects every machine, keeping terminal positions; called when the
    /// app goes to the background, where iOS would drop the sockets anyway.
    func suspendAll() {
        active = false
        for m in machines { m.suspend() }
    }

    /// Stores a new pairing. Pairing the same machine again replaces it.
    @discardableResult
    func add(_ record: MachineRecord) throws -> MachineClient {
        try store.save(record)
        if let existing = machine(record.machineID) {
            existing.suspend()
            machines.removeAll { $0 === existing }
        }
        let client = makeClient(record)
        machines.append(client)
        if active { client.resume() }
        return client
    }

    /// Revokes this phone's token on the machine if it can be reached, then
    /// forgets the machine. Returns whether the agent confirmed the revocation.
    @discardableResult
    func remove(_ machine: MachineClient) async throws -> Bool {
        var revoked = false
        if machine.record.pushLinkID != nil {
            await push.disable(machine)
        }
        if machine.state == .online {
            revoked = (try? await machine.unpair()) != nil
        }
        machine.suspend()
        try store.delete(machineID: machine.id)
        machines.removeAll { $0 === machine }
        return revoked
    }

    private func save(_ machine: MachineClient, pushLinkID: String?) throws {
        var r = machine.record
        r.pushLinkID = pushLinkID
        try store.save(r)
        machine.update(record: r)
    }

    static var defaultDeviceName: String {
        // Without a special entitlement this is the model ("iPhone"), not the
        // user's device name; the pairing screen lets them change it.
        UIDevice.current.name
    }

    // MARK: Notifications

    /// At launch: if notifications are allowed, register with APNs so the
    /// token (and Radix's copy of it) stays current.
    func registerForPushIfAllowed() async {
        guard push.isAvailable else { return }
        let settings = await UNUserNotificationCenter.current().notificationSettings()
        if [.authorized, .provisional, .ephemeral].contains(settings.authorizationStatus) {
            UIApplication.shared.registerForRemoteNotifications()
        }
    }

    func deviceTokenChanged(_ token: Data) async {
        do {
            if try await push.deviceTokenChanged(token, environment: Self.apnsEnvironment) {
                // A new registration has no links; relink what had notifications on.
                needsRelink = Set(machines.filter { $0.record.pushLinkID != nil }.map(\.id))
                for m in machines where m.state == .online { await repairPush(m) }
            }
            pushError = nil
        } catch {
            pushError = error.localizedDescription
        }
        resumeTokenWaiters()
    }

    func deviceTokenFailed(_ error: Error) {
        pushError = error.localizedDescription
        resumeTokenWaiters()
    }

    private func resumeTokenWaiters() {
        let waiters = tokenWaiters
        tokenWaiters = []
        for w in waiters { w.resume() }
    }

    /// Turns notifications from `machine` on (asking for permission the first
    /// time) or off. `prefs` switches individual kinds off.
    func setNotifications(_ on: Bool, for machine: MachineClient, prefs: [String: Bool]? = nil) async throws {
        guard on else {
            await push.disable(machine)
            try save(machine, pushLinkID: nil)
            return
        }
        if push.device == nil {
            let allowed = try await UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound, .badge])
            guard allowed else { throw PushError.notRegistered }
            await withCheckedContinuation { c in
                tokenWaiters.append(c)
                UIApplication.shared.registerForRemoteNotifications()
                // iOS doesn't promise an answer (no network, say); don't wait forever.
                Task {
                    try? await Task.sleep(for: .seconds(20))
                    self.resumeTokenWaiters()
                }
            }
            guard push.device != nil else {
                throw NotificationSetupError(message: pushError ?? "Couldn't register with Apple's push service. Try again later.")
            }
        }
        let linkID = try await push.enable(machine, prefs: prefs)
        try save(machine, pushLinkID: linkID)
    }

    /// When a machine connects: relink it if Radix forgot this phone, or if
    /// the agent lost the route or had its token rejected.
    private func repairPush(_ machine: MachineClient) async {
        guard machine.record.pushLinkID != nil, push.device != nil else { return }
        do {
            if needsRelink.remove(machine.id) != nil {
                let prefs = try? await machine.pushStatus().prefs
                try save(machine, pushLinkID: try await push.enable(machine, prefs: prefs))
            } else if let linkID = try await push.repairIfNeeded(machine) {
                try save(machine, pushLinkID: linkID)
            }
        } catch {
            pushError = error.localizedDescription
        }
    }

    /// Tapping a notification opens its session.
    func open(_ target: NotificationTarget) {
        guard machine(target.machineID) != nil else { return }
        pendingRoute = [.machine(target.machineID), .session(machineID: target.machineID, sessionID: target.sessionID)]
    }

    /// The reply action on a notification: connect, type the text and press
    /// Enter, but only if Claude is still on the turn the notification was
    /// about. Runs in the background with a few seconds to spare, so on any
    /// failure it posts a "not sent" notification instead.
    func reply(_ text: String, to target: NotificationTarget) async {
        guard let m = machine(target.machineID) else {
            return await notifyNotSent(target, why: "That machine is no longer paired with this phone.")
        }
        defer { if !active { m.suspend() } }
        do {
            try await m.sendWhenOnline(sessionID: target.sessionID, text: text, ifTurn: target.turn)
        } catch let e as ProtoError where e.code == ProtoError.stale {
            await notifyNotSent(target, why: "Claude had already moved on, so your reply wasn't typed.")
        } catch is NotConnected {
            await notifyNotSent(target, why: "Couldn't reach \(m.record.name). Is Tailscale connected?")
        } catch {
            await notifyNotSent(target, why: error.localizedDescription)
        }
    }

    private func notifyNotSent(_ target: NotificationTarget, why: String) async {
        let content = UNMutableNotificationContent()
        content.title = "Reply not sent"
        content.body = "\(why) Tap to open the session."
        content.threadIdentifier = target.threadID
        content.userInfo = target.userInfo
        content.sound = .default
        try? await UNUserNotificationCenter.current()
            .add(UNNotificationRequest(identifier: UUID().uuidString, content: content, trigger: nil))
    }
}

struct NotificationSetupError: Error, LocalizedError {
    var message: String
    var errorDescription: String? { message }
}
