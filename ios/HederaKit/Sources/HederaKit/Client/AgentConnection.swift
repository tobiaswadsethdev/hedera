import Foundation

/// What an `AgentConnection` passes up besides responses.
public enum ConnectionEvent: Sendable {
    case event(Event)
    case lagged(Lagged)
    case detached(Detached)
    case eventsLagged(EventsLagged)
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
        receiver = Task { [weak self] in await self?.receiveLoop() }
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
            Task {
                do {
                    try await transport.send(.text(data))
                } catch {
                    pending.removeValue(forKey: id)?.continuation.resume(throwing: error)
                }
            }
        }
        return try JSONDecoder().decode(R.Result.self, from: raw)
    }

    public func sendInput(channel: UInt32, _ bytes: Data) {
        guard !closed, !bytes.isEmpty else { return }
        let transport = self.transport
        var rest = bytes[...]
        while !rest.isEmpty {
            let chunk = rest.prefix(Proto.maxPayload)
            rest = rest.dropFirst(chunk.count)
            let frame = Frame.input(channel: channel, payload: Data(chunk)).encode()
            Task { try? await transport.send(.binary(frame)) }
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
            case .unknown: break
            }
        }
    }

    private func finish(_ error: TransportError) {
        guard !closed else { return }
        closed = true
        receiver?.cancel()
        let waiting = pending
        pending = [:]
        for p in waiting.values { p.continuation.resume(throwing: error) }
        onEvent(.closed(error))
    }
}
