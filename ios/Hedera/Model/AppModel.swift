import Foundation
import HederaKit
import Observation
import UIKit

/// The paired machines, one `MachineClient` (and one WebSocket) each.
@MainActor
@Observable
final class AppModel {
    private(set) var machines: [MachineClient] = []
    var loadError: String?

    @ObservationIgnored private let store: MachineStore
    @ObservationIgnored private let transport: TransportFactory
    @ObservationIgnored private var active = false

    static let clientInfo = ClientInfo(
        name: "Hedera-iOS",
        version: Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "0",
        platform: "ios")

    init(store: MachineStore = KeychainMachineStore(), transport: @escaping TransportFactory = URLSessionTransport.factory) {
        self.store = store
        self.transport = transport
        do {
            machines = try store.loadAll().map(makeClient)
        } catch {
            loadError = error.localizedDescription
        }
    }

    private func makeClient(_ record: MachineRecord) -> MachineClient {
        MachineClient(record: record, clientInfo: Self.clientInfo, transport: transport)
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
        if machine.state == .online {
            revoked = (try? await machine.unpair()) != nil
        }
        machine.suspend()
        try store.delete(machineID: machine.id)
        machines.removeAll { $0 === machine }
        return revoked
    }

    static var defaultDeviceName: String {
        // Without a special entitlement this is the model ("iPhone"), not the
        // user's device name; the pairing screen lets them change it.
        UIDevice.current.name
    }
}
