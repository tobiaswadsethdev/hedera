import Foundation
import Testing
@testable import HederaKit

@MainActor
@Suite struct TranscriptTests {
    private func client(_ agent: FakeAgent) async throws -> MachineClient {
        let c = MachineClient(record: agent.record,
                              clientInfo: ClientInfo(name: "test", version: "0", platform: "ios"),
                              transport: { agent.makeTransport($0, $1) },
                              backoff: Backoff(base: .milliseconds(1), cap: .milliseconds(4), floor: .milliseconds(1)),
                              pingInterval: .seconds(3600))
        c.resume()
        try await eventually("online") { c.state == .online }
        return c
    }

    private func texts(_ f: TranscriptFeed) -> [String] {
        f.entries.compactMap { $0.blocks?.first?.text }
    }

    @Test func tailThenLiveEntries() async throws {
        let agent = FakeAgent()
        let sid = agent.addSession(kind: "claude")
        for i in 1...300 { agent.say(sid, "p\(i)") }
        let c = try await client(agent)
        let f = c.openTranscript(sessionID: sid)
        try await eventually("tail") { f.entries.count == 200 }
        #expect(f.entries.first?.seq == 101)
        #expect(f.hasOlder)
        #expect(agent.requests("transcript.subscribe").last?["tail"] as? Int == 200)

        agent.say(sid, "live")
        try await eventually("live entry") { texts(f).last == "live" }
        #expect(c.openTranscript(sessionID: sid) === f)
        c.suspend()
    }

    @Test func reconnectResumesWithoutRepeats() async throws {
        let agent = FakeAgent()
        let sid = agent.addSession(kind: "claude")
        agent.say(sid, "a")
        let c = try await client(agent)
        let f = c.openTranscript(sessionID: sid)
        try await eventually("first") { f.entries.count == 1 }

        agent.dropConnections()
        agent.say(sid, "b") // written while the phone was away
        try await eventually("caught up") { texts(f) == ["a", "b"] }
        #expect(agent.requests("transcript.subscribe").last?["since"] as? Int == 1)
        c.suspend()
    }

    @Test func loadsOlderEntries() async throws {
        let agent = FakeAgent()
        let sid = agent.addSession(kind: "claude")
        for i in 1...250 { agent.say(sid, "p\(i)") }
        let c = try await client(agent)
        let f = c.openTranscript(sessionID: sid)
        try await eventually("tail") { f.entries.count == 200 }
        await f.loadOlder()
        #expect(f.entries.count == 250)
        #expect(f.entries.map(\.seq) == Array(1...250))
        #expect(!f.hasOlder)
        c.suspend()
    }

    @Test func startsOverAfterAnAgentRestart() async throws {
        let agent = FakeAgent()
        let sid = agent.addSession(kind: "claude")
        agent.say(sid, "old")
        let c = try await client(agent)
        let f = c.openTranscript(sessionID: sid)
        try await eventually("first") { f.entries.count == 1 }

        // Same session ID after the restart only to show the feed is reset,
        // not matched up by sequence number.
        agent.restart()
        agent.sessions[sid] = FakeAgent.FakeSession(session: Session(
            id: sid, kind: "claude", title: "t", cwd: "/", argv: ["claude"], pid: 1, createdAt: "2026-10-27T21:00:00.000Z",
            size: TermSize(cols: 80, rows: 24), status: "idle", outputOffset: 0, attached: 0, claude: nil, exit: nil))
        agent.say(sid, "new")
        try await eventually("fresh transcript") { texts(f) == ["new"] }
        c.suspend()
    }

    @Test func waitsForTheTranscriptToAppear() async throws {
        let agent = FakeAgent()
        let sid = agent.addSession(kind: "claude")
        let c = try await client(agent)
        let f = c.openTranscript(sessionID: sid)
        try await eventually("unavailable") { f.unavailable }

        agent.say(sid, "hello")
        var s = agent.sessions[sid]!.session
        s.claude = ClaudeState(status: "working", sessionID: "abc", turn: 1, needsInput: nil, lastMessagePreview: nil,
                               lastError: nil, endReason: nil, transcript: true)
        agent.sessions[sid]!.session = s
        agent.emit(type: Event.sessionUpdated, session: s)
        try await eventually("subscribed once it exists") { texts(f) == ["hello"] }
        #expect(!f.unavailable)
        c.suspend()
    }

    @Test func closeUnsubscribes() async throws {
        let agent = FakeAgent()
        let sid = agent.addSession(kind: "claude")
        agent.say(sid, "a")
        let c = try await client(agent)
        let f = c.openTranscript(sessionID: sid)
        try await eventually("first") { f.entries.count == 1 }
        f.close()
        try await eventually("unsubscribed") { !agent.requests("transcript.unsubscribe").isEmpty }
        #expect(c.openTranscript(sessionID: sid) !== f)
        c.suspend()
    }
}
