// An in-process Radix (protocol/radix-api.md), served through URLProtocol so
// RadixClient runs unchanged. Each FakeRadix gets its own URLSession, tagged
// with a header that routes its requests here, so tests can run in parallel.

import Foundation
@testable import HederaKit

final class FakeRadix: @unchecked Sendable {
    struct Request {
        var method: String
        var path: String
        var authorization: String?
        var body: [String: Any]
    }

    let url = URL(string: "https://radix.test")!
    private let id = UUID().uuidString
    private let lock = NSLock()
    private var _requests: [Request] = []
    private var devices: [String: (secret: String, token: String)] = [:]
    private var links: [String: (device: String, token: String)] = [:]
    private var counter = 0

    var requests: [Request] { lock.withLock { _requests } }
    func requests(_ method: String, _ prefix: String) -> [Request] {
        requests.filter { $0.method == method && $0.path.hasPrefix(prefix) }
    }
    var linkIDs: [String] { lock.withLock { links.keys.sorted() } }
    func pushToken(_ linkID: String) -> String? { lock.withLock { links[linkID]?.token } }
    func forgetDevices() { lock.withLock { devices = [:] } }

    var session: URLSession {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [FakeRadixProtocol.self]
        config.httpAdditionalHeaders = ["X-Fake-Radix": id]
        return URLSession(configuration: config)
    }

    init() {
        FakeRadixProtocol.register(self, id: id)
    }

    deinit {
        FakeRadixProtocol.unregister(id)
    }

    func handle(_ req: URLRequest) -> (Int, Any?) {
        let body = req.httpBodyData.flatMap { try? JSONSerialization.jsonObject(with: $0) as? [String: Any] } ?? [:]
        let auth = req.value(forHTTPHeaderField: "Authorization")
        let path = req.url!.path
        let method = req.httpMethod ?? "GET"
        return lock.withLock {
            _requests.append(Request(method: method, path: path, authorization: auth, body: body))
            counter += 1
            let parts = path.split(separator: "/").map(String.init) // ["v1", "devices", id?]
            func device() -> String? {
                devices.first { "Bearer \($0.value.secret)" == auth }?.key
            }
            func failure(_ status: Int, _ code: String) -> (Int, Any?) {
                (status, ["error": ["code": code, "message": code]])
            }
            switch (method, parts.dropFirst().first, parts.count) {
            case ("POST", "devices", 2):
                let id = "d_\(counter)"
                devices[id] = ("hrd_secret\(counter)", body["apns_token"] as? String ?? "")
                return (201, ["device_id": id, "device_secret": "hrd_secret\(counter)"])
            case ("PUT", "devices", 3):
                guard let d = device(), d == parts[2] else { return failure(401, "unauthorized") }
                devices[d]!.token = body["apns_token"] as? String ?? ""
                return (200, ["device_id": d])
            case ("DELETE", "devices", 3):
                guard let d = device(), d == parts[2] else { return failure(401, "unauthorized") }
                devices[d] = nil
                links = links.filter { $0.value.device != d }
                return (204, nil)
            case ("POST", "machines", 2):
                guard let d = device() else { return failure(401, "unauthorized") }
                let id = "l_\(counter)"
                links[id] = (d, "hrp_token\(counter)")
                return (201, ["link_id": id, "push_token": "hrp_token\(counter)"])
            case ("DELETE", "machines", 3):
                guard let d = device(), links[parts[2]]?.device == d else { return failure(404, "not_found") }
                links[parts[2]] = nil
                return (204, nil)
            default:
                return failure(404, "not_found")
            }
        }
    }
}

final class FakeRadixProtocol: URLProtocol, @unchecked Sendable {
    nonisolated(unsafe) private static var servers: [String: FakeRadix] = [:]
    private static let lock = NSLock()

    static func register(_ s: FakeRadix, id: String) { lock.withLock { servers[id] = s } }
    static func unregister(_ id: String) { _ = lock.withLock { servers.removeValue(forKey: id) } }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        let id = request.value(forHTTPHeaderField: "X-Fake-Radix") ?? ""
        guard let server = Self.lock.withLock({ Self.servers[id] }) else {
            client?.urlProtocol(self, didFailWithError: URLError(.cannotConnectToHost))
            return
        }
        let (status, body) = server.handle(request)
        let response = HTTPURLResponse(url: request.url!, statusCode: status, httpVersion: "HTTP/1.1",
                                       headerFields: ["Content-Type": "application/json"])!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        if let body { client?.urlProtocol(self, didLoad: try! JSONSerialization.data(withJSONObject: body)) }
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}
}

extension URLRequest {
    /// URLSession moves bodies into a stream before URLProtocol sees them.
    var httpBodyData: Data? {
        if let httpBody { return httpBody }
        guard let stream = httpBodyStream else { return nil }
        stream.open()
        defer { stream.close() }
        var data = Data()
        var buffer = [UInt8](repeating: 0, count: 4096)
        while stream.hasBytesAvailable {
            let n = stream.read(&buffer, maxLength: buffer.count)
            if n <= 0 { break }
            data.append(buffer, count: n)
        }
        return data
    }
}
