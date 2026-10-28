import Foundation
import Network

/// One HTTP/1.1 request over pinned TLS, for the two plain requests the app
/// makes: redeeming a pairing code, and asking why a WebSocket upgrade failed.
enum PinnedHTTP {
    struct Response: Equatable {
        var status: Int
        var body: Data
    }

    static func request(host: String, port: String, pin: String, method: String, path: String,
                        headers: [(String, String)] = [], body: Data? = nil,
                        timeout: Duration = .seconds(10)) async throws -> Response {
        let check = PinCheck()
        guard let nwPort = NWEndpoint.Port(port) else { throw TransportError.failed("bad port \(port)") }
        let connection = NWConnection(host: NWEndpoint.Host(host), port: nwPort,
                                      using: PinnedTLS.parameters(pin: pin, check: check))
        var head = "\(method) \(path) HTTP/1.1\r\nHost: \(host.urlHost):\(port)\r\nConnection: close\r\n"
        for (name, value) in headers { head += "\(name): \(value)\r\n" }
        if let body { head += "Content-Length: \(body.count)\r\n" }
        head += "\r\n"
        let exchange = Exchange(connection: connection, check: check, request: Data(head.utf8) + (body ?? Data()))
        return try await withTaskCancellationHandler {
            try await exchange.run(timeout: timeout)
        } onCancel: {
            connection.cancel()
        }
    }

    /// Parses a complete response read until the server closed the connection.
    static func parse(_ data: Data) throws -> Response {
        let bytes = [UInt8](data)
        guard let split = bytes.firstRange(of: [13, 10, 13, 10]) else {
            throw TransportError.failed("incomplete HTTP response")
        }
        let head = String(decoding: bytes[..<split.lowerBound], as: UTF8.self)
        var lines = head.components(separatedBy: "\r\n")
        let statusLine = lines.removeFirst().split(separator: " ")
        guard statusLine.count >= 2, statusLine[0].hasPrefix("HTTP/1."), let status = Int(statusLine[1]) else {
            throw TransportError.failed("malformed HTTP status line")
        }
        var fields: [String: String] = [:]
        for line in lines {
            guard let colon = line.firstIndex(of: ":") else { continue }
            fields[line[..<colon].lowercased()] = line[line.index(after: colon)...].trimmingCharacters(in: .whitespaces)
        }
        var body = Array(bytes[split.upperBound...])
        if fields["transfer-encoding"]?.lowercased() == "chunked" {
            body = try dechunk(body)
        } else if let length = fields["content-length"].flatMap(Int.init) {
            guard body.count >= length else { throw TransportError.failed("truncated HTTP body") }
            body = Array(body.prefix(length))
        }
        return Response(status: status, body: Data(body))
    }

    private static func dechunk(_ b: [UInt8]) throws -> [UInt8] {
        var out: [UInt8] = []
        var pos = 0
        while true {
            guard let eol = b[pos...].firstRange(of: [13, 10]),
                  let size = Int(String(decoding: b[pos..<eol.lowerBound], as: UTF8.self)
                      .split(separator: ";").first ?? "", radix: 16)
            else { throw TransportError.failed("malformed chunked body") }
            if size == 0 { return out }
            let start = eol.upperBound
            guard start + size <= b.count else { throw TransportError.failed("truncated chunked body") }
            out += b[start..<start + size]
            pos = start + size + 2
        }
    }

    /// The connection's callbacks all run on `queue`, which owns the state.
    private final class Exchange: @unchecked Sendable {
        private let connection: NWConnection
        private let check: PinCheck
        private let request: Data
        private let queue = DispatchQueue(label: "hedera.http")
        private var buffer = Data()
        private var continuation: CheckedContinuation<Response, Error>?

        init(connection: NWConnection, check: PinCheck, request: Data) {
            self.connection = connection
            self.check = check
            self.request = request
        }

        func run(timeout: Duration) async throws -> Response {
            try await withCheckedThrowingContinuation { c in
                queue.async { [self] in
                    continuation = c
                    connection.stateUpdateHandler = { [self] state in
                        switch state {
                        case .ready: send()
                        case let .waiting(error), let .failed(error): finish(.failure(translate(error)))
                        case .cancelled: finish(.failure(translate(nil)))
                        default: break
                        }
                    }
                    connection.start(queue: queue)
                    let seconds = Double(timeout.components.seconds) + Double(timeout.components.attoseconds) / 1e18
                    queue.asyncAfter(deadline: .now() + seconds) { [self] in finish(.failure(TransportError.timeout)) }
                }
            }
        }

        private func send() {
            connection.send(content: request, completion: .contentProcessed { [self] error in
                if let error { finish(.failure(translate(error))) } else { read() }
            })
        }

        private func read() {
            connection.receive(minimumIncompleteLength: 1, maximumLength: 64 * 1024) { [self] data, _, isComplete, error in
                if let data { buffer.append(data) }
                if buffer.count > 1 << 20 {
                    finish(.failure(TransportError.failed("HTTP response too large")))
                } else if isComplete {
                    finish(Result { try PinnedHTTP.parse(buffer) })
                } else if let error {
                    finish(.failure(translate(error)))
                } else {
                    read()
                }
            }
        }

        private func translate(_ error: NWError?) -> Error {
            if check.outcome == .mismatched { return TransportError.pinMismatch }
            return TransportError.failed(error?.localizedDescription ?? "connection cancelled")
        }

        private func finish(_ result: Result<Response, Error>) {
            guard let c = continuation else { return }
            continuation = nil
            connection.stateUpdateHandler = nil
            connection.cancel()
            c.resume(with: result)
        }
    }
}
