import Foundation

public enum PairingError: Error, LocalizedError, Equatable {
    case codeRejected
    case tooManyAttempts
    case pinMismatch
    case unreachable(String)
    case unexpected(Int)

    public var errorDescription: String? {
        switch self {
        case .codeRejected: "The pairing code was already used or has expired. Run hedera pair again."
        case .tooManyAttempts: "Too many failed attempts. Wait a few minutes and try again."
        case .pinMismatch: "The machine's certificate doesn't match the pairing code. Nothing was sent."
        case let .unreachable(why): "Couldn't reach the machine: \(why). Is Tailscale connected?"
        case let .unexpected(status): "The machine answered with HTTP \(status)."
        }
    }
}

public enum Pairing {
    struct Request: Encodable {
        var code: String
        var deviceName: String
        enum CodingKeys: String, CodingKey {
            case code
            case deviceName = "device_name"
        }
    }

    struct Response: Decodable {
        var pairingID: String
        var token: String
        var machineID: String
        var name: String
        enum CodingKeys: String, CodingKey {
            case token, name
            case pairingID = "pairing_id"
            case machineID = "machine_id"
        }
    }

    /// Redeems a pairing code (protocol/spec.md section 1.3), trying each
    /// address in turn. The certificate is pinned to the URL's fingerprint
    /// before the code leaves the phone.
    public static func pair(_ url: PairingURL, deviceName: String) async throws -> MachineRecord {
        let body = try JSONEncoder().encode(Request(code: url.code, deviceName: deviceName))
        var lastError: Error = PairingError.unreachable("no address")
        for host in url.hosts {
            let response: PinnedHTTP.Response
            do {
                response = try await PinnedHTTP.request(
                    host: host, port: url.port, pin: url.fingerprint, method: "POST", path: "/v1/pair",
                    headers: [("Content-Type", "application/json")], body: body)
            } catch TransportError.pinMismatch {
                // The verify block refused the key. Don't try other
                // addresses with the same code.
                throw PairingError.pinMismatch
            } catch {
                lastError = PairingError.unreachable(error.localizedDescription)
                continue
            }
            switch response.status {
            case 200:
                let r = try JSONDecoder().decode(Response.self, from: response.body)
                return MachineRecord(machineID: r.machineID, name: url.name.isEmpty ? r.name : url.name,
                                     hosts: url.hosts, port: url.port, fingerprint: url.fingerprint,
                                     pairingID: r.pairingID, token: r.token)
            case 401: throw PairingError.codeRejected
            case 429: throw PairingError.tooManyAttempts
            case let status: throw PairingError.unexpected(status)
            }
        }
        throw lastError
    }
}
