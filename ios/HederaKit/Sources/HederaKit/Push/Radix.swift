// The app's side of the Radix API (protocol/radix-api.md): registering this
// phone's APNs token and linking machines to it. Radix has an ordinary
// publicly trusted certificate, so this uses URLSession and the system trust
// store, unlike connections to agents.

import Foundation

public enum APNsEnvironment: String, Codable, Sendable {
    /// Builds run from Xcode.
    case sandbox
    /// TestFlight and App Store builds.
    case production
}

/// This phone as Radix knows it. The secret manages the device and its links,
/// so the whole value lives in the Keychain.
public struct PushDevice: Codable, Equatable, Sendable {
    public var deviceID: String
    public var deviceSecret: String
    public var apnsToken: String
    public var environment: APNsEnvironment

    enum CodingKeys: String, CodingKey {
        case environment
        case deviceID = "device_id"
        case deviceSecret = "device_secret"
        case apnsToken = "apns_token"
    }

    public init(deviceID: String, deviceSecret: String, apnsToken: String, environment: APNsEnvironment) {
        self.deviceID = deviceID
        self.deviceSecret = deviceSecret
        self.apnsToken = apnsToken
        self.environment = environment
    }
}

extension PushDevice: CustomStringConvertible, CustomDebugStringConvertible, CustomReflectable {
    public var description: String { "PushDevice(\(deviceID), \(environment.rawValue))" }
    public var debugDescription: String { description }
    public var customMirror: Mirror {
        Mirror(self, children: ["deviceID": deviceID, "environment": environment, "deviceSecret": "<redacted>"])
    }
}

public struct RadixError: Error, Equatable, LocalizedError {
    public var status: Int
    public var code: String
    public var message: String

    public var errorDescription: String? {
        switch code {
        case "rate_limited": "The notification service is busy. Try again in a few minutes."
        default: "The notification service refused the request: \(message.isEmpty ? code : message)"
        }
    }

    /// The device (or link) no longer exists on Radix, or its secret is wrong.
    public var isGone: Bool { status == 401 || status == 404 }
}

public struct RadixLink: Equatable, Sendable {
    public var linkID: String
    /// Handed to the agent with `push.configure`; not kept on the phone.
    public var pushToken: String
}

public struct RadixClient: Sendable {
    public let baseURL: URL
    private let session: URLSession

    public init(baseURL: URL, session: URLSession? = nil) {
        self.baseURL = baseURL
        self.session = session ?? {
            let config = URLSessionConfiguration.ephemeral
            config.timeoutIntervalForRequest = 15
            config.urlCache = nil
            config.httpCookieStorage = nil
            return URLSession(configuration: config)
        }()
    }

    /// `POST /v1/devices`
    public func registerDevice(apnsToken: String, environment: APNsEnvironment) async throws -> PushDevice {
        struct Created: Decodable {
            var device_id: String
            var device_secret: String
        }
        let r: Created = try await request("POST", "/v1/devices",
                                           body: ["apns_token": apnsToken, "environment": environment.rawValue])
        return PushDevice(deviceID: r.device_id, deviceSecret: r.device_secret, apnsToken: apnsToken, environment: environment)
    }

    /// `PUT /v1/devices/{id}`: iOS handed out a new token, or Radix disabled
    /// the device after APNs refused the old one.
    public func updateDevice(_ device: PushDevice) async throws {
        let _: Empty = try await request("PUT", "/v1/devices/\(device.deviceID)", secret: device.deviceSecret,
                                         body: ["apns_token": device.apnsToken, "environment": device.environment.rawValue])
    }

    /// `DELETE /v1/devices/{id}`, which also removes every link.
    public func deleteDevice(_ device: PushDevice) async throws {
        let _: Empty = try await request("DELETE", "/v1/devices/\(device.deviceID)", secret: device.deviceSecret)
    }

    /// `POST /v1/machines`
    public func link(machineID: String, label: String, device: PushDevice) async throws -> RadixLink {
        struct Linked: Decodable {
            var link_id: String
            var push_token: String
        }
        let r: Linked = try await request("POST", "/v1/machines", secret: device.deviceSecret,
                                          body: ["machine_id": machineID, "label": label])
        return RadixLink(linkID: r.link_id, pushToken: r.push_token)
    }

    /// `DELETE /v1/machines/{id}`; the link's push token stops working.
    public func unlink(_ linkID: String, device: PushDevice) async throws {
        let _: Empty = try await request("DELETE", "/v1/machines/\(linkID)", secret: device.deviceSecret)
    }

    public struct TestResult: Decodable, Equatable, Sendable {
        public var sent: Bool
        public var apnsStatus: Int
        public var apnsReason: String?

        enum CodingKeys: String, CodingKey {
            case sent
            case apnsStatus = "apns_status"
            case apnsReason = "apns_reason"
        }
    }

    /// `POST /v1/devices/{id}/test`
    public func sendTest(device: PushDevice) async throws -> TestResult {
        try await request("POST", "/v1/devices/\(device.deviceID)/test", secret: device.deviceSecret)
    }

    private struct Empty: Decodable {}

    private struct Failure: Decodable {
        struct Detail: Decodable {
            var code: String
            var message: String
        }
        var error: Detail
    }

    private func request<R: Decodable>(_ method: String, _ path: String, secret: String? = nil,
                                       body: [String: String]? = nil) async throws -> R {
        var req = URLRequest(url: baseURL.appending(path: path))
        req.httpMethod = method
        if let secret { req.setValue("Bearer \(secret)", forHTTPHeaderField: "Authorization") }
        if let body {
            req.setValue("application/json", forHTTPHeaderField: "Content-Type")
            req.httpBody = try JSONEncoder().encode(body)
        }
        let (data, response) = try await session.data(for: req)
        let status = (response as? HTTPURLResponse)?.statusCode ?? 0
        guard (200..<300).contains(status) else {
            let detail = (try? JSONDecoder().decode(Failure.self, from: data))?.error
            throw RadixError(status: status, code: detail?.code ?? "http_\(status)", message: detail?.message ?? "")
        }
        if R.self == Empty.self { return Empty() as! R }
        return try JSONDecoder().decode(R.self, from: data)
    }
}

public protocol PushDeviceStore: Sendable {
    func load() throws -> PushDevice?
    func save(_ device: PushDevice) throws
    func delete() throws
}

/// One Keychain item, with the same protections as the machine records.
public struct KeychainPushDeviceStore: PushDeviceStore {
    private let items: KeychainItems

    public init(service: String = "hedera.push") {
        items = KeychainItems(service: service)
    }

    public func load() throws -> PushDevice? {
        try items.get(account: "device").flatMap { try? JSONDecoder().decode(PushDevice.self, from: $0) }
    }

    public func save(_ device: PushDevice) throws {
        try items.set(JSONEncoder().encode(device), account: "device")
    }

    public func delete() throws {
        try items.delete(account: "device")
    }
}

/// For tests and previews.
public final class InMemoryPushDeviceStore: PushDeviceStore, @unchecked Sendable {
    private let lock = NSLock()
    private var device: PushDevice?

    public init(_ device: PushDevice? = nil) {
        self.device = device
    }

    public func load() throws -> PushDevice? { lock.withLock { device } }
    public func save(_ device: PushDevice) throws { lock.withLock { self.device = device } }
    public func delete() throws { lock.withLock { device = nil } }
}
