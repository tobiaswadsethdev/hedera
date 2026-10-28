import Foundation
import Testing
@testable import HederaKit

@MainActor
private func makeClient(_ agent: FakeAgent) -> MachineClient {
    MachineClient(record: agent.record,
                  clientInfo: ClientInfo(name: "test", version: "0", platform: "ios"),
                  transport: { agent.makeTransport($0, $1) },
                  backoff: Backoff(base: .milliseconds(1), cap: .milliseconds(4), floor: .milliseconds(1)),
                  pingInterval: .seconds(3600))
}

@MainActor
@Suite struct ReconnectTests {
    @Test func syncsSessionsAndEvents() async throws {
        let agent = FakeAgent()
        agent.addSession(output: "a")
        let client = makeClient(agent)
        client.resume()
        try await eventually("online") { client.state == .online }
        #expect(client.sessions.map(\.id) == ["s_1"])
        #expect(agent.requests("events.subscribe").first?["since_seq"] == nil)

        agent.addSession()
        try await eventually("live event") { client.sessions.count == 2 }
        agent.remove("s_1")
        try await eventually("removal") { client.sessions.map(\.id) == ["s_2"] }
        client.suspend()
    }

    @Test func reattachesFromLastRenderedOffset() async throws {
        let agent = FakeAgent()
        let sid = agent.addSession(output: "hello ")
        let client = makeClient(agent)
        client.resume()
        try await eventually("online") { client.state == .online }

        let sink = RecordingSink()
        let term = client.openTerminal(sessionID: sid, size: TermSize(cols: 100, rows: 30), sink: sink)
        try await eventually("replay") { sink.string == "hello " }
        agent.write(sid, "world")
        try await eventually("live output") { sink.string == "hello world" }

        // iOS drops the socket; output continues meanwhile.
        agent.dropConnections()
        agent.write(sid, "!!!")
        try await eventually("reconnect") { sink.string == "hello world!!!" && client.state == .online }

        let attaches = agent.requests("session.attach")
        #expect(attaches.count == 2)
        #expect(attaches[0]["from_offset"] == nil)
        #expect((attaches[1]["from_offset"] as? NSNumber)?.intValue == 11)
        #expect((attaches[1]["cols"] as? NSNumber)?.intValue == 100)
        #expect(sink.resets == 0)

        term.send(Data("ls\r".utf8))
        try await eventually("input") { agent.input[sid] == Data("ls\r".utf8) }
        client.suspend()
    }

    @Test func suspendAndResumeLosesNothing() async throws {
        let agent = FakeAgent()
        let sid = agent.addSession(output: "one\r\n")
        let client = makeClient(agent)
        client.resume()
        let sink = RecordingSink()
        _ = client.openTerminal(sessionID: sid, size: nil, sink: sink)
        try await eventually("replay") { sink.string == "one\r\n" }

        client.suspend()
        #expect(client.state == .suspended)
        agent.write(sid, "two\r\n")
        agent.addSession()
        client.resume()
        try await eventually("resumed") { sink.string == "one\r\ntwo\r\n" && client.sessions.count == 2 }
        // The backlog covered the new session, so no second session.list.
        #expect(agent.requests("session.list").count == 1)
        #expect(agent.requests("events.subscribe").last?["since_seq"] as? NSNumber == 1)
        client.suspend()
    }

    @Test func keystrokesArriveInOrder() async throws {
        let agent = FakeAgent()
        let sid = agent.addSession()
        let client = makeClient(agent)
        client.resume()
        let term = client.openTerminal(sessionID: sid, size: nil, sink: RecordingSink())
        try await eventually("attached") { term.isAttached }
        agent.transports.last!.jitter = true
        // Keystrokes reach the terminal from different paths (software
        // keyboard, hardware keys, accessory row) at different priorities.
        let text = "echo hedera | rev\r"
        for (i, ch) in text.enumerated() {
            let priority: TaskPriority = i % 2 == 0 ? .userInitiated : .utility
            Task(priority: priority) { term.send(Data(String(ch).utf8)) }
            await Task.yield()
        }
        try await eventually("all input") { agent.input[sid]?.count == text.utf8.count }
        #expect(String(decoding: agent.input[sid]!, as: UTF8.self) == text)
        client.suspend()
    }

    @Test func duplicateOutputIsTrimmed() async throws {
        let agent = FakeAgent()
        let sid = agent.addSession(output: "abc")
        let client = makeClient(agent)
        client.resume()
        let sink = RecordingSink()
        _ = client.openTerminal(sessionID: sid, size: nil, sink: sink)
        try await eventually("replay") { sink.string == "abc" }
        // Resend an overlapping frame, as a reconnect race might.
        let t = agent.transports.last!
        let ch = t.channels.first!.key
        t.deliver(.binary(Frame.output(channel: ch, offset: 1, payload: Data("bcdef".utf8)).encode()))
        t.deliver(.binary(Frame.output(channel: ch, offset: 0, payload: Data("abc".utf8)).encode()))
        try await eventually("trimmed") { sink.string == "abcdef" }
        try await Task.sleep(for: .milliseconds(10))
        #expect(sink.string == "abcdef")
        client.suspend()
    }

    @Test func resetRebuildsFromSessionList() async throws {
        let agent = FakeAgent()
        agent.eventWindow = 2
        agent.addSession()
        let client = makeClient(agent)
        client.resume()
        try await eventually("online") { client.state == .online }
        client.suspend()
        // More events than the agent keeps.
        agent.addSession()
        agent.addSession()
        agent.addSession()
        client.resume()
        try await eventually("rebuilt") { client.sessions.count == 4 }
        #expect(agent.requests("session.list").count == 2)
        client.suspend()
    }

    @Test func newBootIDRebuildsAndEndsTerminals() async throws {
        let agent = FakeAgent()
        let sid = agent.addSession(output: "x")
        let client = makeClient(agent)
        client.resume()
        let sink = RecordingSink()
        let term = client.openTerminal(sessionID: sid, size: nil, sink: sink)
        try await eventually("replay") { sink.string == "x" }

        agent.restart()
        agent.addSession() // s_2 on the new boot; seq starts over at 1
        try await eventually("rebuilt") { client.sessions.map(\.id) == ["s_2"] && client.state == .online }
        #expect(term.ended == "removed")
        let subs = agent.requests("events.subscribe")
        #expect(subs.last?["since_seq"] == nil)
        #expect(agent.requests("session.list").count == 2)
        client.suspend()
    }

    @Test func laggedReattachesFromItsOffset() async throws {
        let agent = FakeAgent()
        let sid = agent.addSession(output: "start ")
        let client = makeClient(agent)
        client.resume()
        let sink = RecordingSink()
        _ = client.openTerminal(sessionID: sid, size: nil, sink: sink)
        try await eventually("replay") { sink.string == "start " }
        agent.lag(sid, pending: "missed")
        try await eventually("caught up") { sink.string == "start missed" }
        #expect((agent.requests("session.attach").last?["from_offset"] as? NSNumber)?.intValue == 6)
        #expect(sink.resets == 0)
        client.suspend()
    }

    @Test func truncatedReplayResetsTheTerminal() async throws {
        let agent = FakeAgent()
        agent.scrollback = 8
        let sid = agent.addSession(output: "12345")
        let client = makeClient(agent)
        client.resume()
        let sink = RecordingSink()
        _ = client.openTerminal(sessionID: sid, size: nil, sink: sink)
        try await eventually("replay") { sink.string == "12345" }
        client.suspend()
        agent.write(sid, "abcdefghij") // more than the scrollback holds
        client.resume()
        try await eventually("reset replay") { sink.resets == 1 && sink.string == "cdefghij" }
        client.suspend()
    }

    @Test func eventsLaggedResubscribes() async throws {
        let agent = FakeAgent()
        let client = makeClient(agent)
        client.resume()
        try await eventually("online") { client.state == .online }
        agent.addSession()
        try await eventually("event") { client.sessions.count == 1 }
        let t = agent.transports.last!
        t.subscribed = false
        agent.addSession() // seq 2, not delivered
        t.deliver(.text(try JSONEncoder().encode(EventsLagged(seq: 1))))
        try await eventually("backlog") { client.sessions.count == 2 }
        #expect(agent.requests("events.subscribe").last?["since_seq"] as? NSNumber == 1)
        client.suspend()
    }

    @Test func revokedStopsReconnecting() async throws {
        let agent = FakeAgent()
        let client = makeClient(agent)
        client.resume()
        try await eventually("online") { client.state == .online }
        agent.revoke()
        try await eventually("revoked") {
            if case .revoked = client.state { true } else { false }
        }
        let connects = agent.connects
        try await Task.sleep(for: .milliseconds(30))
        #expect(agent.connects == connects)
        client.resume()
        #expect(agent.connects == connects)
    }

    @Test func unauthorizedAtConnectIsPermanent() async throws {
        let agent = FakeAgent()
        agent.refuse = .unauthorized
        let client = makeClient(agent)
        client.resume()
        try await eventually("revoked") {
            if case .revoked = client.state { true } else { false }
        }
    }

    @Test func fallsBackToTheNextAddress() async throws {
        let agent = FakeAgent()
        var record = agent.record
        record.hosts = ["100.64.0.9", "studio.tail.ts.net"]
        agent.unreachableHosts = ["100.64.0.9"]
        let client = MachineClient(record: record, clientInfo: ClientInfo(name: "t", version: "0", platform: nil),
                                   transport: { agent.makeTransport($0, $1) },
                                   backoff: Backoff(base: .milliseconds(1), cap: .milliseconds(4), floor: .milliseconds(1)),
                                   pingInterval: .seconds(3600))
        client.resume()
        try await eventually("online") { client.state == .online }
        client.suspend()
    }

    @Test func requestsFailWhileOffline() async throws {
        let agent = FakeAgent()
        let client = makeClient(agent)
        await #expect(throws: NotConnected.self) { try await client.createSession(SessionCreate(kind: "shell")) }
        client.resume()
        try await eventually("online") { client.state == .online }
        let s = try await client.createSession(SessionCreate(kind: "shell"))
        #expect(client.session(s.id) != nil)
        client.suspend()
    }
}

@Suite struct CursorTests {
    @Test func firstAttachStartsAtReplay() {
        var c = OutputCursor()
        #expect(c.attached(replay: Replay(from: 100, to: 150, truncated: false)) == false)
        #expect(c.accept(offset: 100, payload: Data("x".utf8)) == Data("x".utf8))
        #expect(c.next == 101)
    }

    @Test func trimsOverlapAndDropsRepeats() {
        var c = OutputCursor(next: 5)
        #expect(c.accept(offset: 0, payload: Data("abcde".utf8)) == nil)
        #expect(c.accept(offset: 3, payload: Data("defgh".utf8)) == Data("fgh".utf8))
        #expect(c.next == 8)
        #expect(c.accept(offset: 8, payload: Data()) == nil)
    }

    @Test func resetWhenReplayDoesNotContinue() {
        var c = OutputCursor(next: 10)
        #expect(c.attached(replay: Replay(from: 10, to: 20, truncated: false)) == false)
        #expect(c.attached(replay: Replay(from: 4, to: 20, truncated: true)) == true)
        #expect(c.next == 4)
    }

    @Test func backoffGrowsAndCaps() {
        let b = Backoff()
        #expect(b.delay(attempt: 0, random: 1) == .milliseconds(500))
        #expect(b.delay(attempt: 3, random: 1) == .seconds(4))
        #expect(b.delay(attempt: 50, random: 1) == .seconds(30))
        #expect(b.delay(attempt: 5, random: 0) == .milliseconds(250))
    }
}
