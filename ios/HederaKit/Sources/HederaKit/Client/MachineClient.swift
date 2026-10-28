import Foundation
import Observation

/// Receives a terminal's output. Implemented by the app's terminal view.
@MainActor
public protocol TerminalSink: AnyObject {
    /// Clear the screen and scrollback before the replay that follows.
    func terminalReset()
    func terminalWrite(_ data: Data)
}

public enum ConnectionState: Equatable, Sendable {
    case connecting
    case online
    /// Waiting to retry after `error`.
    case waiting(error: String)
    /// Disconnected on purpose (the app is in the background).
    case suspended
    /// The pairing is gone or the certificate changed; reconnecting won't help.
    case revoked(String)
}

public struct NotConnected: Error, LocalizedError {
    public var errorDescription: String? { "Not connected to the machine." }
}

/// One paired machine: keeps a WebSocket open while the app is active,
/// mirrors the agent's sessions from `events.subscribe`, and keeps terminals
/// attached across reconnects with nothing lost or duplicated.
///
/// Reconnecting (protocol/spec.md sections 4.5, 4.12 and 5.1):
/// - back off with jitter, then re-attach every open terminal from the offset
///   after the last byte it rendered;
/// - re-subscribe with `since_seq`, and rebuild from `session.list` when the
///   agent answers `reset` or reports a new `boot_id`;
/// - on `lagged`, re-attach from the offset it gives;
/// - on `events.lagged`, re-subscribe from its `seq`.
@MainActor
@Observable
public final class MachineClient: Identifiable {
    public private(set) var record: MachineRecord
    public private(set) var state: ConnectionState = .suspended
    public private(set) var sessions: [Session] = []
    public private(set) var hello: HelloResult?
    /// Called each time a connection is up and synced.
    @ObservationIgnored public var onOnline: (() -> Void)?

    public nonisolated var id: String { machineID }
    private nonisolated let machineID: String

    @ObservationIgnored private let makeTransport: TransportFactory
    @ObservationIgnored private let clientInfo: ClientInfo
    @ObservationIgnored private let backoff: Backoff
    @ObservationIgnored private let pingInterval: Duration

    @ObservationIgnored private var connection: AgentConnection?
    @ObservationIgnored private var generation = 0
    @ObservationIgnored private var bootID: String?
    @ObservationIgnored private var lastSeq: UInt64?
    @ObservationIgnored private var attempt = 0
    @ObservationIgnored private var preferredHost: String?
    @ObservationIgnored private var runner: Task<Void, Never>?
    @ObservationIgnored private var sleeper: Task<Void, Never>?
    @ObservationIgnored private var closeError: TransportError?
    @ObservationIgnored private var closeWaiter: CheckedContinuation<TransportError, Never>?
    @ObservationIgnored private var attachments: [ObjectIdentifier: TerminalAttachment] = [:]
    @ObservationIgnored private var feeds: [String: TranscriptFeed] = [:]
    @ObservationIgnored private var channels: [UInt32: TerminalAttachment] = [:]

    public init(record: MachineRecord,
                clientInfo: ClientInfo,
                transport: @escaping TransportFactory = NetworkTransport.factory,
                backoff: Backoff = Backoff(),
                pingInterval: Duration = .seconds(20)) {
        self.record = record
        self.machineID = record.machineID
        self.clientInfo = clientInfo
        self.makeTransport = transport
        self.backoff = backoff
        self.pingInterval = pingInterval
    }

    // MARK: Lifecycle

    /// Connects, and keeps reconnecting until `suspend()`.
    public func resume() {
        if case .revoked = state { return }
        guard runner == nil else {
            reconnectNow()
            return
        }
        attempt = 0
        runner = Task { [weak self] in await self?.run() }
    }

    /// Disconnects and stops reconnecting, keeping every terminal's position so
    /// `resume()` picks up exactly where it left off.
    public func suspend() {
        runner?.cancel()
        runner = nil
        sleeper?.cancel()
        connection?.close()
        connection = nil
        if case .revoked = state { return }
        state = .suspended
    }

    /// Skips the current backoff wait.
    public func reconnectNow() {
        attempt = 0
        sleeper?.cancel()
    }

    public func update(record: MachineRecord) {
        self.record = record
    }

    private func run() async {
        while !Task.isCancelled {
            state = .connecting
            let connectedAt = ContinuousClock.now
            var failure: TransportError
            do {
                try await connectAndSync()
                state = .online
                onOnline?()
                failure = await waitForClose()
                if ContinuousClock.now - connectedAt > .seconds(10) { attempt = 0 }
            } catch let e as TransportError {
                failure = e
            } catch let e as ProtoError {
                failure = .failed(e.message)
            } catch {
                failure = .failed(error.localizedDescription)
            }
            connection = nil
            if Task.isCancelled { return }
            if failure.isPermanent {
                state = .revoked(failure.localizedDescription)
                runner = nil
                return
            }
            state = .waiting(error: failure.localizedDescription)
            let delay = backoff.delay(attempt: attempt)
            attempt += 1
            let sleeper = Task { _ = try? await Task.sleep(for: delay) }
            self.sleeper = sleeper
            await sleeper.value
        }
    }

    private func connectAndSync() async throws {
        closeError = nil
        var hosts = record.hosts
        if let p = preferredHost, let i = hosts.firstIndex(of: p) {
            hosts.remove(at: i)
            hosts.insert(p, at: 0)
        }
        var lastError: Error = TransportError.failed("no address")
        var connected: (AgentConnection, HelloResult)?
        for host in hosts {
            generation += 1
            let gen = generation
            let conn = AgentConnection(transport: makeTransport(record, host)) { [weak self] ev in
                self?.handle(ev, generation: gen)
            }
            do {
                let h = try await conn.handshake(client: clientInfo)
                guard h.machineID == record.machineID else {
                    conn.close()
                    throw TransportError.failed("\(host) is a different machine")
                }
                preferredHost = host
                connected = (conn, h)
                break
            } catch let e as TransportError where e.isPermanent {
                conn.close()
                throw e
            } catch {
                conn.close()
                lastError = error
            }
            try Task.checkCancellation()
        }
        guard let (conn, h) = connected else { throw lastError }
        connection = conn
        hello = h
        if bootID != h.bootID {
            // The agent restarted: sequence numbers start over and every
            // session it had is gone.
            bootID = h.bootID
            lastSeq = nil
            // Transcript numbers start over too.
            for f in feeds.values { f.reset() }
        }
        try await subscribe(conn, since: lastSeq)
        for a in attachments.values where a.ended == nil {
            await attach(a, on: conn, from: a.cursor.resumeOffset)
        }
        for f in feeds.values {
            await subscribe(f, on: conn)
        }
        startPinging(conn)
    }

    private func subscribe(_ conn: AgentConnection, since: UInt64?) async throws {
        let sub = try await conn.call(EventsSubscribe(sinceSeq: since)) { [weak self] sub in
            // Set before any backlog event is handled.
            if since == nil || sub.reset { self?.lastSeq = sub.seq }
        }
        if since == nil || sub.reset {
            sessions = sortSessions(try await conn.call(SessionList()).sessions)
            for a in attachments.values where a.ended == nil && !sessions.contains(where: { $0.id == a.sessionID }) {
                a.end(reason: "removed")
            }
        }
    }

    private func waitForClose() async -> TransportError {
        if let e = closeError { return e }
        return await withCheckedContinuation { closeWaiter = $0 }
    }

    private func startPinging(_ conn: AgentConnection) {
        let interval = pingInterval
        Task { [weak self, weak conn] in
            while true {
                try? await Task.sleep(for: interval)
                guard let self, let conn, self.connection === conn else { return }
                do { try await conn.ping() } catch { conn.close() }
            }
        }
    }

    // MARK: Inbound

    private func handle(_ ev: ConnectionEvent, generation gen: Int) {
        guard gen == generation else { return }
        switch ev {
        case let .output(channel, offset, payload):
            guard let a = channels[channel], let fresh = a.cursor.accept(offset: offset, payload: payload) else { return }
            a.sink?.terminalWrite(fresh)
        case let .event(e):
            if let last = lastSeq, e.seq <= last { return }
            lastSeq = e.seq
            apply(e)
        case let .lagged(l):
            guard let a = channels.removeValue(forKey: l.channel), let conn = connection else { return }
            a.channel = nil
            Task { await attach(a, on: conn, from: l.offset) }
        case let .detached(d):
            guard let a = channels.removeValue(forKey: d.channel) else { return }
            a.channel = nil
            a.end(reason: d.reason)
        case let .eventsLagged(l):
            guard let conn = connection else { return }
            lastSeq = l.seq
            Task { try? await subscribe(conn, since: l.seq) }
        case let .transcript(m):
            feeds[m.sessionID]?.append(m.entries)
        case let .closed(error):
            for a in channels.values { a.channel = nil }
            channels = [:]
            closeError = error
            closeWaiter?.resume(returning: error)
            closeWaiter = nil
        }
    }

    private func apply(_ e: Event) {
        switch e.type {
        case Event.sessionRemoved:
            sessions.removeAll { $0.id == e.sessionID }
            for a in attachments.values where a.sessionID == e.sessionID { a.end(reason: "removed") }
        case Event.sessionResized:
            if let i = sessions.firstIndex(where: { $0.id == e.sessionID }), let c = e.cols, let r = e.rows {
                sessions[i].size = TermSize(cols: c, rows: r)
            }
        default:
            if let s = e.session {
                upsert(s)
                // The transcript became available after the feed was opened.
                if let f = feeds[s.id], f.unavailable, s.claude?.transcript == true, let conn = connection {
                    Task { await subscribe(f, on: conn) }
                }
            }
        }
    }

    private func upsert(_ s: Session) {
        if let i = sessions.firstIndex(where: { $0.id == s.id }) {
            sessions[i] = s
        } else {
            sessions = sortSessions(sessions + [s])
        }
    }

    private func sortSessions(_ s: [Session]) -> [Session] {
        s.sorted { $0.createdAt < $1.createdAt }
    }

    public func session(_ id: String) -> Session? {
        sessions.first { $0.id == id }
    }

    // MARK: Transcripts (protocol/spec.md section 6.2)

    /// The structured transcript of a Claude session. The feed follows the
    /// session across reconnects until `close()`; opening it again returns
    /// the same feed.
    public func openTranscript(sessionID: String) -> TranscriptFeed {
        if let f = feeds[sessionID] { return f }
        let f = TranscriptFeed(sessionID: sessionID, client: self)
        feeds[sessionID] = f
        if state == .online, let conn = connection {
            Task { await subscribe(f, on: conn) }
        }
        return f
    }

    private func subscribe(_ f: TranscriptFeed, on conn: AgentConnection) async {
        let since = f.lastSeq
        do {
            let r = try await conn.call(TranscriptSubscribe(sessionID: f.sessionID, since: since, tail: since == nil ? 200 : nil)) { r in
                // Runs before the backlog is handled.
                f.firstSeq = r.firstSeq
                f.unavailable = false
                f.loaded = true
            }
            if r.reset {
                f.reset()
                await subscribe(f, on: conn)
            }
        } catch let e as ProtoError where e.code == ProtoError.unavailable {
            f.unavailable = true
        } catch {
            // Not found, or the connection dropped; a reconnect subscribes again.
        }
    }

    fileprivate func loadOlder(_ f: TranscriptFeed) async throws {
        guard let first = f.entries.first?.seq, let conn = connection else { return }
        let r = try await conn.call(TranscriptRead(sessionID: f.sessionID, before: first, limit: 100))
        f.prepend(r.entries)
    }

    fileprivate func close(_ f: TranscriptFeed) {
        guard feeds[f.sessionID] === f else { return }
        feeds.removeValue(forKey: f.sessionID)
        if let conn = connection {
            Task { _ = try? await conn.call(TranscriptUnsubscribe(sessionID: f.sessionID)) }
        }
    }

    // MARK: Terminals

    /// Attaches `sink` to a session's output. The attachment survives
    /// reconnects until `close()`.
    public func openTerminal(sessionID: String, size: TermSize?, sink: TerminalSink) -> TerminalAttachment {
        let a = TerminalAttachment(sessionID: sessionID, client: self, sink: sink, size: size)
        attachments[ObjectIdentifier(a)] = a
        if state == .online, let conn = connection {
            Task { await attach(a, on: conn, from: nil) }
        }
        return a
    }

    private func attach(_ a: TerminalAttachment, on conn: AgentConnection, from offset: UInt64?) async {
        let req = SessionAttach(sessionID: a.sessionID, fromOffset: offset, cols: a.size?.cols, rows: a.size?.rows)
        do {
            let r = try await conn.call(req) { [weak self, weak a] r in
                // Runs before the channel's first OUTPUT frame is handled.
                guard let self, let a, self.attachments[ObjectIdentifier(a)] != nil else { return }
                a.channel = r.channel
                self.channels[r.channel] = a
                if a.cursor.attached(replay: r.replay) { a.sink?.terminalReset() }
                self.upsert(r.session)
            }
            if attachments[ObjectIdentifier(a)] == nil, connection === conn {
                // Closed while the attach was in flight.
                _ = try? await conn.call(SessionDetach(channel: r.channel))
            }
        } catch let e as ProtoError where e.code == ProtoError.notFound {
            a.end(reason: "removed")
        } catch {
            // The connection dropped; the next connect re-attaches.
        }
    }

    fileprivate func send(_ bytes: Data, from a: TerminalAttachment) {
        guard let ch = a.channel, let conn = connection else { return }
        conn.sendInput(channel: ch, bytes)
    }

    fileprivate func resize(_ a: TerminalAttachment) {
        guard let size = a.size, a.channel != nil, let conn = connection else { return }
        Task { _ = try? await conn.call(SessionResize(sessionID: a.sessionID, cols: size.cols, rows: size.rows)) }
    }

    fileprivate func close(_ a: TerminalAttachment) {
        attachments.removeValue(forKey: ObjectIdentifier(a))
        guard let ch = a.channel else { return }
        channels.removeValue(forKey: ch)
        a.channel = nil
        if let conn = connection {
            Task { _ = try? await conn.call(SessionDetach(channel: ch)) }
        }
    }

    // MARK: Requests

    private func online() throws -> AgentConnection {
        guard state == .online, let c = connection else { throw NotConnected() }
        return c
    }

    @discardableResult
    public func createSession(_ request: SessionCreate) async throws -> Session {
        let s = try await online().call(request).session
        upsert(s)
        return s
    }

    public func closeSession(_ id: String, kill: Bool = false) async throws {
        _ = try await online().call(SessionClose(sessionID: id, mode: kill ? "kill" : nil))
    }

    public func removeSession(_ id: String) async throws {
        _ = try await online().call(SessionRemove(sessionID: id))
        sessions.removeAll { $0.id == id }
    }

    public func sendInput(sessionID: String, text: String, submit: Bool, ifTurn: Int? = nil) async throws {
        _ = try await online().call(SessionInput(sessionID: sessionID, data: text, submit: submit, ifTurn: ifTurn))
    }

    public func resumable() async throws -> [Resumable] {
        try await online().call(SessionResumable()).resumable
    }

    @discardableResult
    public func resume(_ r: Resumable, size: TermSize?) async throws -> Session {
        let s = try await online().call(SessionResume(claudeSessionID: r.claudeSessionID, cols: size?.cols, rows: size?.rows)).session
        upsert(s)
        return s
    }

    /// Asks the agent to forget this phone's token (protocol/spec.md section 1.4).
    public func unpair() async throws {
        _ = try await online().call(PairingUnpair())
    }

    /// For a notification reply, which runs in the background with little
    /// time: connects if needed, waits until online, then types `text` and
    /// presses Enter. With `ifTurn`, the agent refuses with `stale` if Claude
    /// has moved on since the notification.
    public func sendWhenOnline(sessionID: String, text: String, ifTurn: Int?, timeout: Duration = .seconds(10)) async throws {
        resume()
        let deadline = ContinuousClock.now + timeout
        while state != .online {
            if case let .revoked(why) = state { throw TransportError.failed(why) }
            guard ContinuousClock.now < deadline else { throw NotConnected() }
            try await Task.sleep(for: .milliseconds(50))
        }
        try await sendInput(sessionID: sessionID, text: text, submit: true, ifTurn: ifTurn)
    }

    // MARK: Push (protocol/spec.md section 7)

    /// Whether the agent can send notifications at all.
    public var supportsPush: Bool { hello?.capabilities.contains("push") ?? false }

    /// Whether the agent takes a `push_key` and encrypts notification text.
    public var supportsPushEncryption: Bool { hello?.capabilities.contains("push_e2e") ?? false }

    @discardableResult
    public func configurePush(radixURL: String, pushToken: String, pushKey: String? = nil,
                              prefs: [String: Bool]? = nil) async throws -> PushStatus {
        try await online().call(PushConfigure(radixURL: radixURL, pushToken: pushToken, pushKey: pushKey, prefs: prefs))
    }

    public func pushStatus() async throws -> PushStatus {
        try await online().call(PushStatusRequest())
    }

    public func removePush() async throws {
        _ = try await online().call(PushRemove())
    }
}

/// A terminal attached to one session. Its cursor outlives connections.
@MainActor
public final class TerminalAttachment {
    public let sessionID: String
    /// Why the session's output ended (`exited`, `removed`), once it has.
    public private(set) var ended: String?
    public var onEnded: ((String) -> Void)?

    var cursor = OutputCursor()
    var channel: UInt32?
    var size: TermSize?
    weak var sink: TerminalSink?
    private weak var client: MachineClient?

    init(sessionID: String, client: MachineClient, sink: TerminalSink, size: TermSize?) {
        self.sessionID = sessionID
        self.client = client
        self.sink = sink
        self.size = size
    }

    public var isAttached: Bool { channel != nil }

    /// Keystrokes. Dropped while disconnected rather than replayed late.
    public func send(_ bytes: Data) {
        client?.send(bytes, from: self)
    }

    public func resize(cols: Int, rows: Int) {
        let new = TermSize(cols: cols, rows: rows)
        guard new != size, cols > 0, rows > 0 else { return }
        size = new
        client?.resize(self)
    }

    public func close() {
        client?.close(self)
    }

    func end(reason: String) {
        guard ended == nil else { return }
        ended = reason
        onEnded?(reason)
    }
}

/// A Claude session's transcript entries, oldest first. It keeps what it has
/// across reconnects and resumes from its last entry.
@MainActor
@Observable
public final class TranscriptFeed {
    public let sessionID: String
    public private(set) var entries: [TranscriptEntry] = []
    /// The agent has no transcript for this session yet.
    public fileprivate(set) var unavailable = false
    /// The first subscription has answered; until then, entries are loading.
    public fileprivate(set) var loaded = false
    public private(set) var isLoadingOlder = false
    /// The oldest entry the agent still holds.
    fileprivate(set) var firstSeq: UInt64 = 1
    @ObservationIgnored private weak var client: MachineClient?

    /// Whether `loadOlder()` has anything to fetch.
    public var hasOlder: Bool {
        guard let first = entries.first?.seq else { return false }
        return first > firstSeq
    }

    var lastSeq: UInt64? { entries.last?.seq }

    init(sessionID: String, client: MachineClient) {
        self.sessionID = sessionID
        self.client = client
    }

    func append(_ new: [TranscriptEntry]) {
        let last = lastSeq ?? 0
        let fresh = new.filter { $0.seq > last }
        if !fresh.isEmpty { entries.append(contentsOf: fresh) }
    }

    func prepend(_ old: [TranscriptEntry]) {
        let first = entries.first?.seq ?? .max
        let older = old.filter { $0.seq < first }
        if !older.isEmpty { entries.insert(contentsOf: older, at: 0) }
    }

    func reset() {
        entries = []
        firstSeq = 1
    }

    /// Fetches up to 100 entries before the oldest one shown.
    public func loadOlder() async {
        guard hasOlder, !isLoadingOlder, let client else { return }
        isLoadingOlder = true
        defer { isLoadingOlder = false }
        try? await client.loadOlder(self)
    }

    public func close() {
        client?.close(self)
    }
}

