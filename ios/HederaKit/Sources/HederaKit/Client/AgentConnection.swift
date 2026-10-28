import Foundation

/// What an `AgentConnection` passes up besides responses.
public enum ConnectionEvent: Sendable {
    case event(Event)
    case lagged(Lagged)
    case detached(Detached)
    case eventsLagged(EventsLagged)
    case transcript(TranscriptMessage)
    case output(channel: UInt32, offset: UInt64, payload: Data)
    case closed(TransportError)
}

/// One live WebSocket to an agent: request/response matching and dispatch of
/// everything else, in the order it arrived. It does not reconnect; that is
/// `MachineClient`'s job.
@MainActor
public final class AgentConnection {
    public private(set) var hello: HelloResult?

    private let transport: WebSocketTransport
    private var nextID: UInt64 = 1
    private var pending: [UInt64: Pending] = [:]
    private var receiver: Task<Void, Never>?
    private var sender: Task<Void, Never>?
    private let outbox: AsyncStream<WSMessage>.Continuation
    private var closed = false
    private let onEvent: (ConnectionEvent) -> Void

    private struct Pending {
        var continuation: CheckedContinuation<Data, Error>
        /// Runs in the receive loop before any later message is handled.
        var onSuccess: ((Data) -> Void)?
    }

    public init(transport: WebSocketTransport, onEvent: @escaping (ConnectionEvent) -> Void) {
        self.transport = transport
        self.onEvent = onEvent
        let (stream, outbox) = AsyncStream.makeStream(of: WSMessage.self)
        self.outbox = outbox
        receiver = Task { [weak self] in await self?.receiveLoop() }
        sender = Task { [weak self] in await self?.sendLoop(stream) }
    }

    /// Sends queued messages one at a time, in the order they were queued.
    /// Keystrokes from separate Tasks could otherwise overtake each other.
    private func sendLoop(_ stream: AsyncStream<WSMessage>) async {
        for await message in stream {
            do {
                try await transport.send(message)
            } catch {
                finish(error as? TransportError ?? .failed(error.localizedDescription))
                return
            }
        }
    }

    /// Sends `hello` and fails unless the agent agrees on a version within `timeout`.
    public func handshake(client: ClientInfo, timeout: Duration = .seconds(10)) async throws -> HelloResult {
        let result = try await abortingAfter(timeout) { try await self.call(Hello(client: client)) }
        hello = result
        return result
    }

    /// Sends a request and waits for its response. `onSuccess` sees the raw
    /// response before the receive loop handles the next message, which lets
    /// `session.attach` claim its channel ahead of the channel's first frame.
    public func call<R: AgentRequest>(_ request: R, onSuccess: ((R.Result) -> Void)? = nil) async throws -> R.Result {
        guard !closed else { throw TransportError.closed(1000) }
        let id = nextID
        nextID += 1
        let data = try Wire.encodeRequest(request, id: id)
        let raw: Data = try await withCheckedThrowingContinuation { c in
            pending[id] = Pending(continuation: c, onSuccess: onSuccess.map { hook in
                { raw in
                    if let r = try? JSONDecoder().decode(R.Result.self, from: raw) { hook(r) }
                }
            })
            outbox.yield(.text(data))
        }
        return try JSONDecoder().decode(R.Result.self, from: raw)
    }

    public func sendInput(channel: UInt32, _ bytes: Data) {
        guard !closed, !bytes.isEmpty else { return }
        var rest = bytes[...]
        while !rest.isEmpty {
            let chunk = rest.prefix(Proto.maxPayload)
            rest = rest.dropFirst(chunk.count)
            outbox.yield(.binary(Frame.input(channel: channel, payload: Data(chunk)).encode()))
        }
    }

    public func ping(timeout: Duration = .seconds(5)) async throws {
        try await abortingAfter(timeout) { try await self.transport.sendPing() }
    }

    public func close(code: Int = 1000) {
        abort(code: code, .closed(code))
    }

    private func abort(code: Int, _ error: TransportError) {
        guard !closed else { return }
        transport.close(code: code)
        finish(error)
    }

    /// Runs `body`; if it hasn't finished after `timeout`, closes the
    /// connection, which fails `body` and every other pending request.
    private func abortingAfter<T>(_ timeout: Duration, _ body: () async throws -> T) async throws -> T {
        let timer = Task { [weak self] in
            guard (try? await Task.sleep(for: timeout)) != nil else { return }
            self?.abort(code: 1000, .timeout)
        }
        defer { timer.cancel() }
        return try await body()
    }

    private func receiveLoop() async {
        while !closed {
            let message: WSMessage
            do {
                message = try await transport.receive()
            } catch {
                finish(error as? TransportError ?? .failed(error.localizedDescription))
                return
            }
            handle(message)
        }
    }

    private func handle(_ message: WSMessage) {
        switch message {
        case let .binary(data):
            guard case let .output(channel, offset, payload)? = try? Frame.decode(data) else {
                close(code: CloseCode.protocolViolation)
                return
            }
            onEvent(.output(channel: channel, offset: offset, payload: payload))
        case let .text(data):
            guard let inbound = try? Wire.decodeInbound(data) else { return }
            switch inbound {
            case let .response(header, raw):
                guard let p = pending.removeValue(forKey: header.re) else { return }
                if header.ok {
                    p.onSuccess?(raw)
                    p.continuation.resume(returning: raw)
                } else {
                    p.continuation.resume(throwing: header.error ?? ProtoError(code: ProtoError.internalError, message: "request failed"))
                }
            case let .event(e): onEvent(.event(e))
            case let .lagged(l): onEvent(.lagged(l))
            case let .detached(d): onEvent(.detached(d))
            case let .eventsLagged(l): onEvent(.eventsLagged(l))
            case let .transcript(m): onEvent(.transcript(m))
            case .unknown: break
            }
        }
    }

    private func finish(_ error: TransportError) {
        guard !closed else { return }
        closed = true
        receiver?.cancel()
        outbox.finish()
        sender?.cancel()
        let waiting = pending
        pending = [:]
        for p in waiting.values { p.continuation.resume(throwing: error) }
        onEvent(.closed(error))
    }
}
