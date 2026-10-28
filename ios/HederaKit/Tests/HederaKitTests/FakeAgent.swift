// An in-process stand-in for the Go agent, following protocol/spec.md closely
// enough to exercise reconnects: attach with replay and truncation, event
// backlog with since_seq and reset, boot IDs, lagged channels, revocation.

import Foundation
@testable import HederaKit

@MainActor
final class FakeAgent {
    struct FakeSession {
        var session: Session
        /// Output still in the scrollback buffer; its first byte is at `bufferStart`.
        var buffer = Data()
        var bufferStart: UInt64 = 0
        var end: UInt64 { bufferStart + UInt64(buffer.count) }
    }

    let machineID = "m_fake"
    var bootID = "b_one"
    var seq: UInt64 = 0
    var events: [Event] = []
    /// How many recent events the agent can replay for since_seq.
    var eventWindow = 100
    var sessions: [String: FakeSession] = [:]
    var scrollback = 1 << 20
    var transports: [FakeTransport] = []
    var refuse: TransportError?
    var unreachableHosts: Set<String> = []

    /// Every request the agent received, as decoded JSON.
    var requests: [[String: Any]] = []
    var input: [String: Data] = [:]
    var connects = 0
    private var nextChannel: UInt32 = 1
    private var sessionCounter = 0

    func requests(_ t: String) -> [[String: Any]] { requests.filter { $0["t"] as? String == t } }

    func makeTransport(_ record: MachineRecord, _ host: String) -> WebSocketTransport {
        let t = FakeTransport(agent: self)
        if unreachableHosts.contains(host) {
            t.fail(.failed("unreachable"))
        } else if let refuse {
            t.fail(refuse)
        } else {
            connects += 1
            transports.append(t)
        }
        return t
    }

    var record: MachineRecord {
        MachineRecord(machineID: machineID, name: "fake", hosts: ["100.64.0.1"], port: "7373",
                      fingerprint: "sha256:x", pairingID: "p_1", token: "hdt_secret")
    }

    // MARK: Driving the agent

    @discardableResult
    func addSession(kind: String = "shell", output: String = "") -> String {
        sessionCounter += 1
        let id = "s_\(sessionCounter)"
        let s = Session(id: id, kind: kind, title: "t\(sessionCounter)", cwd: "/", argv: ["sh"], pid: 100 + sessionCounter,
                        createdAt: String(format: "2026-10-27T21:%02d:00.000Z", sessionCounter),
                        size: TermSize(cols: 80, rows: 24), status: "running", outputOffset: 0, attached: 0,
                        claude: nil, exit: nil)
        sessions[id] = FakeSession(session: s)
        emit(type: Event.sessionCreated, session: s)
        if !output.isEmpty { write(id, output) }
        return id
    }

    func write(_ id: String, _ text: String) {
        let bytes = Data(text.utf8)
        var s = sessions[id]!
        let offset = s.end
        s.buffer.append(bytes)
        if s.buffer.count > scrollback {
            let drop = s.buffer.count - scrollback
            s.buffer.removeFirst(drop)
            s.bufferStart += UInt64(drop)
        }
        s.session.outputOffset = s.end
        sessions[id] = s
        for t in transports {
            for (ch, sid) in t.channels where sid == id {
                t.deliver(.binary(Frame.output(channel: ch, offset: offset, payload: bytes).encode()))
            }
        }
    }

    func emit(type: String, session: Session? = nil, sessionID: String? = nil) {
        seq += 1
        let e = Event(seq: seq, ts: "2026-10-27T21:00:00.000Z", type: type, session: session, sessionID: sessionID)
        events.append(e)
        if events.count > eventWindow { events.removeFirst(events.count - eventWindow) }
        for t in transports where t.subscribed {
            t.deliver(.text(try! JSONEncoder().encode(e)))
        }
    }

    func remove(_ id: String) {
        sessions[id] = nil
        for t in transports {
            for (ch, sid) in t.channels where sid == id {
                t.channels[ch] = nil
                t.deliver(.text(try! JSONEncoder().encode(Detached(channel: ch, sessionID: id, reason: "removed"))))
            }
        }
        emit(type: Event.sessionRemoved, sessionID: id)
    }

    /// Drops every connection, as iOS does in the background.
    func dropConnections() {
        for t in transports { t.fail(.failed("network connection lost")) }
        transports = []
    }

    /// Restarts the agent: new boot ID, sequence numbers from 1, no sessions.
    func restart() {
        dropConnections()
        bootID = "b_" + UUID().uuidString.prefix(8)
        seq = 0
        events = []
        sessions = [:]
    }

    /// Ends a channel as if the client fell behind, after writing `pending`
    /// that the client never receives.
    func lag(_ id: String, pending: String) {
        let t = transports.last!
        let (ch, _) = t.channels.first { $0.value == id }!
        t.channels[ch] = nil
        let offset = sessions[id]!.end
        write(id, pending)
        t.deliver(.text(try! JSONEncoder().encode(Lagged(channel: ch, sessionID: id, offset: offset))))
    }

    func revoke() {
        for t in transports { t.fail(.closed(CloseCode.revoked)) }
        transports = []
        refuse = .unauthorized
    }

    // MARK: Requests

    func handle(_ data: Data, from t: FakeTransport) {
        let obj = try! JSONSerialization.jsonObject(with: data) as! [String: Any]
        requests.append(obj)
        let id = (obj["id"] as! NSNumber).uint64Value
        func reply<R: Encodable>(_ r: R) { t.deliver(.text(try! JSONEncoder().encode(ResultEnvelope(re: id, result: r)))) }
        func fail(_ code: String) {
            t.deliver(.text(try! JSONEncoder().encode(ResponseHeader(re: id, ok: false, error: ProtoError(code: code, message: code)))))
        }
        switch obj["t"] as! String {
        case "hello":
            let h = HelloResult(protocol: 1, machineID: machineID, bootID: bootID,
                                agent: AgentInfo(version: "0", os: "darwin", arch: "arm64", hostname: "fake"),
                                capabilities: ["shell"],
                                limits: Limits(maxSessions: 32, scrollbackBytes: scrollback, maxFrame: 65536),
                                serverTime: "2026-10-27T21:00:00.000Z")
            reply(h)
        case "session.list":
            reply(SessionList.Result(sessions: sessions.values.map(\.session).sorted { $0.createdAt < $1.createdAt }))
        case "events.subscribe":
            t.subscribed = true
            let since = (obj["since_seq"] as? NSNumber)?.uint64Value
            let oldest = events.first?.seq ?? seq + 1
            if let since, since + 1 >= oldest || since == seq {
                reply(EventsSubscribe.Result(seq: seq, reset: false))
                for e in events where e.seq > since { t.deliver(.text(try! JSONEncoder().encode(e))) }
            } else {
                reply(EventsSubscribe.Result(seq: seq, reset: since != nil))
            }
        case "session.attach":
            let sid = obj["session_id"] as! String
            guard let s = sessions[sid] else { return fail(ProtoError.notFound) }
            var from = (obj["from_offset"] as? NSNumber)?.uint64Value ?? s.bufferStart
            var truncated = false
            if from < s.bufferStart || from > s.end {
                from = s.bufferStart
                truncated = true
            }
            let ch = nextChannel
            nextChannel += 1
            reply(SessionAttach.Result(channel: ch, session: s.session,
                                       replay: Replay(from: from, to: s.end, truncated: truncated)))
            let replay = s.buffer.dropFirst(Int(from - s.bufferStart))
            if !replay.isEmpty {
                t.deliver(.binary(Frame.output(channel: ch, offset: from, payload: Data(replay)).encode()))
            }
            t.channels[ch] = sid
        case "session.detach":
            t.channels[(obj["channel"] as! NSNumber).uint32Value] = nil
            reply(EmptyResult())
        case "session.resize":
            reply(EmptyResult())
        case "session.create":
            let sid = addSession()
            reply(SessionCreate.Result(session: sessions[sid]!.session))
        default:
            fail(ProtoError.unknownType)
        }
    }

    func input(_ data: Data, from t: FakeTransport) {
        guard case let .input(ch, payload) = try! Frame.decode(data), let sid = t.channels[ch] else { return }
        input[sid, default: Data()].append(payload)
    }
}

@MainActor
final class FakeTransport: WebSocketTransport {
    weak var agent: FakeAgent?
    var channels: [UInt32: String] = [:]
    var subscribed = false
    private var queue: [WSMessage] = []
    private var waiter: CheckedContinuation<WSMessage, Error>?
    private var failure: TransportError?

    init(agent: FakeAgent) {
        self.agent = agent
    }

    func deliver(_ m: WSMessage) {
        guard failure == nil else { return }
        if let w = waiter {
            waiter = nil
            w.resume(returning: m)
        } else {
            queue.append(m)
        }
    }

    func fail(_ e: TransportError) {
        guard failure == nil else { return }
        failure = e
        queue = []
        channels = [:]
        waiter?.resume(throwing: e)
        waiter = nil
    }

    func send(_ message: WSMessage) async throws {
        if let failure { throw failure }
        switch message {
        case let .text(d): agent?.handle(d, from: self)
        case let .binary(d): agent?.input(d, from: self)
        }
    }

    func receive() async throws -> WSMessage {
        if !queue.isEmpty { return queue.removeFirst() }
        if let failure { throw failure }
        return try await withCheckedThrowingContinuation { waiter = $0 }
    }

    func sendPing() async throws {
        if let failure { throw failure }
    }

    func close(code: Int) {
        fail(.closed(code))
        agent?.transports.removeAll { $0 === self }
    }
}

@MainActor
final class RecordingSink: TerminalSink {
    var text = Data()
    var resets = 0

    func terminalReset() {
        resets += 1
        text = Data()
    }

    func terminalWrite(_ data: Data) {
        text.append(data)
    }

    var string: String { String(decoding: text, as: UTF8.self) }
}

/// Polls `condition` until it holds or two seconds pass.
@MainActor
func eventually(_ what: String = "", _ condition: () -> Bool) async throws {
    let deadline = ContinuousClock.now + .seconds(2)
    while !condition() {
        if ContinuousClock.now > deadline {
            throw TimedOut(what: what)
        }
        try await Task.sleep(for: .milliseconds(1))
    }
}

struct TimedOut: Error, CustomStringConvertible {
    var what: String
    var description: String { "timed out waiting for \(what)" }
}
