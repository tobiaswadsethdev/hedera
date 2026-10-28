import Foundation
import Testing
@testable import HederaKit

@MainActor
private func onlineClient(_ agent: FakeAgent, linkID: String? = nil) async throws -> MachineClient {
    var record = agent.record
    record.pushLinkID = linkID
    let client = MachineClient(record: record,
                               clientInfo: ClientInfo(name: "test", version: "0", platform: "ios"),
                               transport: { agent.makeTransport($0, $1) },
                               backoff: Backoff(base: .milliseconds(1), cap: .milliseconds(4), floor: .milliseconds(1)),
                               pingInterval: .seconds(3600))
    client.resume()
    try await eventually("online") { client.state == .online }
    return client
}

private let token = Data([0xAB, 0x01, 0xFF] + [UInt8](repeating: 0, count: 29))
private let tokenHex = "ab01ff" + String(repeating: "00", count: 29)

@MainActor
@Suite struct PushTests {
    @Test func registersOnceThenUpdates() async throws {
        let radix = FakeRadix()
        let store = InMemoryPushDeviceStore()
        let push = PushManager(radixURL: radix.url, store: store, keys: InMemoryPushKeyStore(), session: radix.session)

        #expect(try await push.deviceTokenChanged(token, environment: .production))
        let created = radix.requests("POST", "/v1/devices")
        #expect(created.count == 1)
        #expect(created.first?.body["apns_token"] as? String == tokenHex)
        #expect(created.first?.body["environment"] as? String == "production")
        #expect(try store.load() == push.device)

        // Every later launch updates, which also re-enables a disabled device.
        #expect(try await !push.deviceTokenChanged(token, environment: .production))
        #expect(radix.requests("PUT", "/v1/devices/").count == 1)
        #expect(radix.requests("PUT", "/v1/devices/").first?.authorization == "Bearer \(push.device!.deviceSecret)")
        #expect(radix.requests("POST", "/v1/devices").count == 1)
    }

    @Test func registersAgainWhenRadixForgotTheDevice() async throws {
        let radix = FakeRadix()
        let push = PushManager(radixURL: radix.url, store: InMemoryPushDeviceStore(), keys: InMemoryPushKeyStore(), session: radix.session)
        try await push.deviceTokenChanged(token, environment: .sandbox)
        let first = push.device
        radix.forgetDevices()
        #expect(try await push.deviceTokenChanged(token, environment: .sandbox))
        #expect(push.device != first)
    }

    @Test func enableLinksAndConfiguresTheAgent() async throws {
        let radix = FakeRadix()
        let agent = FakeAgent()
        let push = PushManager(radixURL: radix.url, store: InMemoryPushDeviceStore(), keys: InMemoryPushKeyStore(), session: radix.session)
        try await push.deviceTokenChanged(token, environment: .production)
        let client = try await onlineClient(agent)

        let linkID = try await push.enable(client, prefs: ["claude_turn_finished": false])
        #expect(radix.linkIDs == [linkID])
        #expect(radix.requests("POST", "/v1/machines").first?.body["machine_id"] as? String == agent.machineID)
        // The agent gets the link's push token, never the device secret.
        #expect(agent.pushRoute?.radixURL == "https://radix.test")
        #expect(agent.pushRoute?.token == radix.pushToken(linkID))
        #expect(agent.pushRoute?.prefs == ["claude_turn_finished": false])
        client.suspend()
    }

    @Test func enableReplacesTheOldLink() async throws {
        let radix = FakeRadix()
        let agent = FakeAgent()
        let push = PushManager(radixURL: radix.url, store: InMemoryPushDeviceStore(), keys: InMemoryPushKeyStore(), session: radix.session)
        try await push.deviceTokenChanged(token, environment: .production)
        let first = try await push.enable(try await onlineClient(agent))
        let client = try await onlineClient(agent, linkID: first)
        let second = try await push.enable(client)
        #expect(radix.linkIDs == [second])
        client.suspend()
    }

    @Test func aRefusedConfigureDeletesTheLink() async throws {
        let radix = FakeRadix()
        let agent = FakeAgent()
        agent.rejectPushTokens = true
        let push = PushManager(radixURL: radix.url, store: InMemoryPushDeviceStore(), keys: InMemoryPushKeyStore(), session: radix.session)
        try await push.deviceTokenChanged(token, environment: .production)
        let client = try await onlineClient(agent)
        await #expect(throws: ProtoError.self) { try await push.enable(client) }
        #expect(radix.linkIDs.isEmpty)
        client.suspend()
    }

    @Test func enableNeedsRegistrationAndAPushCapableAgent() async throws {
        let radix = FakeRadix()
        let push = PushManager(radixURL: radix.url, store: InMemoryPushDeviceStore(), keys: InMemoryPushKeyStore(), session: radix.session)
        let agent = FakeAgent()
        let client = try await onlineClient(agent)
        await #expect(throws: PushError.notRegistered) { try await push.enable(client) }

        try await push.deviceTokenChanged(token, environment: .production)
        let old = FakeAgent()
        old.capabilities = ["shell"]
        let oldClient = try await onlineClient(old)
        await #expect(throws: PushError.unsupported) { try await push.enable(oldClient) }

        let none = PushManager(radixURL: nil, store: InMemoryPushDeviceStore(), keys: InMemoryPushKeyStore())
        await #expect(throws: PushError.unavailable) { try await none.enable(client) }
        client.suspend()
        oldClient.suspend()
    }

    @Test func disableRemovesTheRouteAndTheLink() async throws {
        let radix = FakeRadix()
        let agent = FakeAgent()
        let push = PushManager(radixURL: radix.url, store: InMemoryPushDeviceStore(), keys: InMemoryPushKeyStore(), session: radix.session)
        try await push.deviceTokenChanged(token, environment: .production)
        let linkID = try await push.enable(try await onlineClient(agent))
        let client = try await onlineClient(agent, linkID: linkID)

        await push.disable(client)
        #expect(agent.pushRoute == nil)
        #expect(radix.linkIDs.isEmpty)
        client.suspend()
    }

    @Test func repairRelinksWhenTheAgentLostOrBrokeTheRoute() async throws {
        let radix = FakeRadix()
        let agent = FakeAgent()
        let push = PushManager(radixURL: radix.url, store: InMemoryPushDeviceStore(), keys: InMemoryPushKeyStore(), session: radix.session)
        try await push.deviceTokenChanged(token, environment: .production)
        let linkID = try await push.enable(try await onlineClient(agent), prefs: ["shell_exited": false])
        let client = try await onlineClient(agent, linkID: linkID)

        #expect(try await push.repairIfNeeded(client) == nil)

        agent.pushBroken = "radix rejected the token"
        let repaired = try #require(try await push.repairIfNeeded(client))
        #expect(repaired != linkID)
        #expect(radix.linkIDs == [repaired])
        #expect(agent.pushRoute?.prefs == ["shell_exited": false])
        client.suspend()
    }

    @Test func forgetDeviceDeletesItEverywhere() async throws {
        let radix = FakeRadix()
        let store = InMemoryPushDeviceStore()
        let push = PushManager(radixURL: radix.url, store: store, keys: InMemoryPushKeyStore(), session: radix.session)
        try await push.deviceTokenChanged(token, environment: .production)
        await push.forgetDevice()
        #expect(push.device == nil)
        #expect(try store.load() == nil)
        #expect(radix.requests("DELETE", "/v1/devices/").count == 1)
    }

    @Test func radixErrorsCarryTheirCode() async throws {
        let radix = FakeRadix()
        let client = RadixClient(baseURL: radix.url, session: radix.session)
        let stranger = PushDevice(deviceID: "d_x", deviceSecret: "hrd_wrong", apnsToken: tokenHex, environment: .sandbox)
        await #expect(throws: RadixError(status: 401, code: "unauthorized", message: "unauthorized")) {
            try await client.updateDevice(stranger)
        }
    }

    @Test func deviceNeverPrintsItsSecret() {
        let d = PushDevice(deviceID: "d_1", deviceSecret: "hrd_canary", apnsToken: tokenHex, environment: .production)
        var dumped = ""
        dump(d, to: &dumped)
        for s in [d.description, d.debugDescription, "\(d)", dumped] {
            #expect(!s.contains("hrd_canary"))
        }
    }
}

/// The notification reply path: connect from suspended, type, press Enter.
@MainActor
@Suite struct ReplyTests {
    private func client(_ agent: FakeAgent) -> MachineClient {
        MachineClient(record: agent.record,
                      clientInfo: ClientInfo(name: "test", version: "0", platform: "ios"),
                      transport: { agent.makeTransport($0, $1) },
                      backoff: Backoff(base: .milliseconds(1), cap: .milliseconds(4), floor: .milliseconds(1)),
                      pingInterval: .seconds(3600))
    }

    private func claudeSession(_ agent: FakeAgent, turn: Int) -> String {
        let id = agent.addSession(kind: "claude")
        agent.sessions[id]!.session.claude = ClaudeState(status: "needs_input", sessionID: nil, turn: turn, needsInput: nil,
                                                         lastMessagePreview: nil, lastError: nil, endReason: nil)
        return id
    }

    @Test func connectsAndTypesTheReply() async throws {
        let agent = FakeAgent()
        let sid = claudeSession(agent, turn: 7)
        let c = client(agent)
        #expect(c.state == .suspended)
        try await c.sendWhenOnline(sessionID: sid, text: "yes, go ahead", ifTurn: 7)
        #expect(agent.input[sid] == Data("yes, go ahead\r".utf8))
        c.suspend()
    }

    @Test func aStaleTurnIsRefused() async throws {
        let agent = FakeAgent()
        let sid = claudeSession(agent, turn: 8)
        let c = client(agent)
        let error = await #expect(throws: ProtoError.self) {
            try await c.sendWhenOnline(sessionID: sid, text: "too late", ifTurn: 7)
        }
        #expect(error?.code == ProtoError.stale)
        #expect(agent.input[sid] == nil)
        c.suspend()
    }

    @Test func givesUpWhenTheMachineIsUnreachable() async throws {
        let agent = FakeAgent()
        agent.unreachableHosts = ["100.64.0.1"]
        let c = client(agent)
        await #expect(throws: NotConnected.self) {
            try await c.sendWhenOnline(sessionID: "s_1", text: "x", ifTurn: nil, timeout: .milliseconds(200))
        }
        c.suspend()
    }
}

/// protocol/fixtures/push-crypto.json, which the Go tests check too.
@Test func pushCryptoFixtures() throws {
    let fixtures = try loadFixture("push-crypto.json")
    #expect(fixtures.count >= 6)
    for f in fixtures {
        let name = f["name"] as! String
        let key = try #require(Data(base64URL: f["key"] as! String))
        let open = {
            try PushCrypto.open(f["encrypted"] as! String, key: key, machineID: f["machine_id"] as! String,
                                kind: f["kind"] as! String, sessionID: f["session_id"] as? String,
                                turn: (f["turn"] as? NSNumber)?.intValue)
        }
        if f["fails"] as? Bool == true {
            #expect(throws: PushCrypto.Failure.authentication, "\(name)") { try open() }
            continue
        }
        let text = try open()
        let expected = try JSONDecoder().decode(PushCrypto.Text.self, from: Data((f["plaintext"] as! String).utf8))
        #expect(text == expected, "\(name)")
        #expect(text.title == f["title"] as? String || (f["title"] as! String).count > 100, "\(name)")
    }
}

@Test func openingAPayloadUsesTheMachinesKey() throws {
    let f = try loadFixture("push-crypto.json")[0]
    let keys = InMemoryPushKeyStore()
    var hedera: [String: Any] = ["kind": f["kind"]!, "machine_id": f["machine_id"]!, "session_id": f["session_id"]!,
                                 "turn": f["turn"]!, "enc": f["encrypted"]!]
    #expect(PushCrypto.open(payload: ["hedera": hedera], keys: keys) == nil) // no key yet
    try keys.save(try #require(Data(base64URL: f["key"] as! String)), machineID: f["machine_id"] as! String)
    #expect(PushCrypto.open(payload: ["hedera": hedera], keys: keys)?.title == "fix flaky test")
    hedera["enc"] = nil
    #expect(PushCrypto.open(payload: ["hedera": hedera], keys: keys) == nil) // plaintext notification
    #expect(PushCrypto.newKey().count == 32)
    #expect(PushCrypto.newKey() != PushCrypto.newKey())
}

@MainActor
@Suite struct PushKeyTests {
    @Test func enableGivesTheAgentAFreshKeyAndKeepsIt() async throws {
        let radix = FakeRadix()
        let agent = FakeAgent()
        let keys = InMemoryPushKeyStore()
        let push = PushManager(radixURL: radix.url, store: InMemoryPushDeviceStore(), keys: keys, session: radix.session)
        try await push.deviceTokenChanged(token, environment: .production)
        _ = try await push.enable(try await onlineClient(agent))
        let first = try #require(agent.pushRoute?.key)
        #expect(Data(base64URL: first) == (try keys.key(machineID: agent.machineID)))
        #expect(Data(base64URL: first)?.count == 32)

        let client = try await onlineClient(agent)
        _ = try await push.enable(client)
        #expect(agent.pushRoute?.key != first) // reconfiguring rotates it
        #expect(Data(base64URL: agent.pushRoute!.key!) == (try keys.key(machineID: agent.machineID)))

        await push.disable(client)
        #expect(try keys.key(machineID: agent.machineID) == nil)
        client.suspend()
    }

    @Test func olderAgentsGetNoKey() async throws {
        let radix = FakeRadix()
        let agent = FakeAgent()
        agent.capabilities = ["shell", "push"]
        let keys = InMemoryPushKeyStore()
        let push = PushManager(radixURL: radix.url, store: InMemoryPushDeviceStore(), keys: keys, session: radix.session)
        try await push.deviceTokenChanged(token, environment: .production)
        let linkID = try await push.enable(try await onlineClient(agent))
        #expect(agent.pushRoute != nil && agent.pushRoute?.key == nil)
        #expect(try keys.key(machineID: agent.machineID) == nil)
        // And no endless relinking because the route has no key.
        let client = try await onlineClient(agent, linkID: linkID)
        #expect(try await push.repairIfNeeded(client) == nil)
        client.suspend()
    }

    @Test func repairAddsAKeyToAnUnencryptedRoute() async throws {
        let radix = FakeRadix()
        let agent = FakeAgent()
        let keys = InMemoryPushKeyStore()
        let push = PushManager(radixURL: radix.url, store: InMemoryPushDeviceStore(), keys: keys, session: radix.session)
        try await push.deviceTokenChanged(token, environment: .production)
        let linkID = try await push.enable(try await onlineClient(agent), prefs: ["shell_exited": false])
        let client = try await onlineClient(agent, linkID: linkID)
        #expect(try await push.repairIfNeeded(client) == nil)

        // A route set up before encryption, or a phone that lost its key.
        try keys.delete(machineID: agent.machineID)
        let relinked = try #require(try await push.repairIfNeeded(client))
        #expect(relinked != linkID)
        #expect(agent.pushRoute?.key != nil)
        #expect(try keys.key(machineID: agent.machineID) != nil)
        #expect(agent.pushRoute?.prefs == ["shell_exited": false])
        client.suspend()
    }

    @Test func aRefusedConfigureKeepsTheOldKey() async throws {
        let radix = FakeRadix()
        let agent = FakeAgent()
        let keys = InMemoryPushKeyStore()
        let push = PushManager(radixURL: radix.url, store: InMemoryPushDeviceStore(), keys: keys, session: radix.session)
        try await push.deviceTokenChanged(token, environment: .production)
        _ = try await push.enable(try await onlineClient(agent))
        let before = try keys.key(machineID: agent.machineID)
        agent.rejectPushTokens = true
        let client = try await onlineClient(agent)
        await #expect(throws: ProtoError.self) { try await push.enable(client) }
        #expect(try keys.key(machineID: agent.machineID) == before)
        client.suspend()
    }
}
