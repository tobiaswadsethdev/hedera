import Foundation

public enum WSMessage: Equatable, Sendable {
    case text(Data)
    case binary(Data)
}

public enum TransportError: Error, Equatable, LocalizedError {
    /// The upgrade was refused with 401: the pairing token is no longer valid.
    case unauthorized
    /// The agent closed the connection with this WebSocket close code.
    case closed(Int)
    /// The TLS handshake was aborted because the server key isn't the pinned one.
    case pinMismatch
    case timeout
    case failed(String)

    public var errorDescription: String? {
        switch self {
        case .unauthorized: "The machine no longer accepts this phone's pairing."
        case let .closed(code): "Connection closed (\(code))."
        case .pinMismatch: "The machine's certificate changed. Re-pair it to trust the new one."
        case .timeout: "The machine didn't answer in time."
        case let .failed(why): why
        }
    }

    /// Errors after which reconnecting cannot help.
    public var isPermanent: Bool {
        switch self {
        case .unauthorized, .pinMismatch, .closed(CloseCode.revoked): true
        default: false
        }
    }
}

/// One WebSocket connection to an agent.
@MainActor
public protocol WebSocketTransport: AnyObject {
    func send(_ message: WSMessage) async throws
    /// The next message; throws a `TransportError` once the connection is gone.
    func receive() async throws -> WSMessage
    func sendPing() async throws
    func close(code: Int)
}

/// Opens a transport to `host` for `record`.
public typealias TransportFactory = @MainActor (_ record: MachineRecord, _ host: String) -> WebSocketTransport

/// `URLSessionWebSocketTask` with the subprotocol, bearer token and pinning
/// the agent requires (protocol/spec.md section 1).
@MainActor
public final class URLSessionTransport: WebSocketTransport {
    private let session: URLSession
    private let task: URLSessionWebSocketTask
    private var closedLocally: Int?

    public init(record: MachineRecord, host: String) {
        let delegate = PinnedSessionDelegate(pin: record.fingerprint)
        session = delegate.makeSession()
        var req = URLRequest(url: URL(string: "wss://\(host.urlHost):\(record.port)/v1/ws")!)
        req.timeoutInterval = 10
        req.setValue(Proto.subprotocol, forHTTPHeaderField: "Sec-WebSocket-Protocol")
        // Sent only after the delegate has accepted the pinned certificate.
        req.setValue("Bearer \(record.token)", forHTTPHeaderField: "Authorization")
        task = session.webSocketTask(with: req)
        task.maximumMessageSize = Proto.maxTextMessage + 1024
        task.resume()
    }

    public static let factory: TransportFactory = { URLSessionTransport(record: $0, host: $1) }

    public func send(_ message: WSMessage) async throws {
        do {
            switch message {
            case let .text(d): try await task.send(.string(String(decoding: d, as: UTF8.self)))
            case let .binary(d): try await task.send(.data(d))
            }
        } catch {
            throw translate(error)
        }
    }

    public func receive() async throws -> WSMessage {
        do {
            switch try await task.receive() {
            case let .string(s): return .text(Data(s.utf8))
            case let .data(d): return .binary(d)
            @unknown default: throw TransportError.failed("unknown WebSocket message")
            }
        } catch {
            throw translate(error)
        }
    }

    public func sendPing() async throws {
        let task = self.task
        try await withCheckedThrowingContinuation { (c: CheckedContinuation<Void, Error>) in
            task.sendPing { error in
                if let error { c.resume(throwing: error) } else { c.resume() }
            }
        }
    }

    public func close(code: Int) {
        closedLocally = closedLocally ?? code
        task.cancel(with: URLSessionWebSocketTask.CloseCode(rawValue: code) ?? .normalClosure, reason: nil)
        session.invalidateAndCancel()
    }

    private func translate(_ error: Error) -> TransportError {
        if let e = error as? TransportError { return e }
        if let code = closedLocally { return .closed(code) }
        if let http = task.response as? HTTPURLResponse, http.statusCode == 401 { return .unauthorized }
        if task.closeCode != .invalid { return .closed(task.closeCode.rawValue) }
        if let u = error as? URLError {
            // Our pinning delegate cancels the challenge, which surfaces as
            // .cancelled before any HTTP response exists.
            if u.code == .cancelled, task.response == nil { return .pinMismatch }
            if u.code == .timedOut { return .timeout }
        }
        return .failed(error.localizedDescription)
    }
}
