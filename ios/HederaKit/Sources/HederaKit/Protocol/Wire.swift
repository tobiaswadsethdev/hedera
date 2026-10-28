// Envelopes for control messages, and the binary PTY frame codec
// (protocol/spec.md section 2).

import Foundation

// MARK: - Control messages

private struct TypeKey: CodingKey {
    var stringValue: String
    var intValue: Int? { nil }
    init(_ s: String) { stringValue = s }
    init?(stringValue: String) { self.stringValue = stringValue }
    init?(intValue: Int) { nil }
}

/// Encodes {"t": type, "id": id} and the request's own fields side by side.
struct RequestEnvelope<R: AgentRequest>: Encodable {
    var id: UInt64
    var request: R

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: TypeKey.self)
        try c.encode(R.type, forKey: TypeKey("t"))
        try c.encode(id, forKey: TypeKey("id"))
        try request.encode(to: encoder)
    }
}

/// Encodes a success response; used by tests and fakes standing in for the agent.
public struct ResultEnvelope<R: Encodable>: Encodable {
    public var re: UInt64
    public var result: R

    public init(re: UInt64, result: R) {
        self.re = re
        self.result = result
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: TypeKey.self)
        try c.encode("res", forKey: TypeKey("t"))
        try c.encode(re, forKey: TypeKey("re"))
        try c.encode(true, forKey: TypeKey("ok"))
        try result.encode(to: encoder)
    }
}

/// The common fields of a response.
public struct ResponseHeader: Codable, Equatable, Sendable {
    public var t = "res"
    public var re: UInt64
    public var ok: Bool
    public var error: ProtoError?

    public init(re: UInt64, ok: Bool, error: ProtoError?) {
        self.re = re
        self.ok = ok
        self.error = error
    }
}

public enum Wire {
    public static func encodeRequest<R: AgentRequest>(_ request: R, id: UInt64) throws -> Data {
        try JSONEncoder().encode(RequestEnvelope(id: id, request: request))
    }

    /// The `t` field of a control message.
    public static func messageType(_ data: Data) throws -> String {
        struct T: Decodable { var t: String }
        return try JSONDecoder().decode(T.self, from: data).t
    }

    /// Splits an agent-to-client control message into its kind.
    public static func decodeInbound(_ data: Data) throws -> Inbound {
        let d = JSONDecoder()
        switch try messageType(data) {
        case "res": return .response(try d.decode(ResponseHeader.self, from: data), data)
        case "event": return .event(try d.decode(Event.self, from: data))
        case "lagged": return .lagged(try d.decode(Lagged.self, from: data))
        case "detached": return .detached(try d.decode(Detached.self, from: data))
        case "events.lagged": return .eventsLagged(try d.decode(EventsLagged.self, from: data))
        case "transcript": return .transcript(try d.decode(TranscriptMessage.self, from: data))
        case let t: return .unknown(t)
        }
    }
}

public enum Inbound: Sendable {
    /// A response; the raw message holds the result fields.
    case response(ResponseHeader, Data)
    case event(Event)
    case lagged(Lagged)
    case detached(Detached)
    case eventsLagged(EventsLagged)
    case transcript(TranscriptMessage)
    /// A message type this build doesn't know; ignored per spec section 9.
    case unknown(String)
}

// MARK: - Binary frames

public enum Frame: Equatable, Sendable {
    case output(channel: UInt32, offset: UInt64, payload: Data)
    case input(channel: UInt32, payload: Data)

    public static let opOutput: UInt8 = 0x01
    public static let opInput: UInt8 = 0x02

    public struct Malformed: Error {}

    public func encode() -> Data {
        var d = Data()
        switch self {
        case let .output(channel, offset, payload):
            d.reserveCapacity(13 + payload.count)
            d.append(Frame.opOutput)
            d.appendBigEndian(channel)
            d.appendBigEndian(offset)
            d.append(payload)
        case let .input(channel, payload):
            d.reserveCapacity(5 + payload.count)
            d.append(Frame.opInput)
            d.appendBigEndian(channel)
            d.append(payload)
        }
        return d
    }

    /// Parses a binary frame, rejecting unknown opcodes, truncated headers and
    /// payloads over 64 KiB, exactly as the agent does.
    public static func decode(_ data: Data) throws -> Frame {
        let b = [UInt8](data)
        guard let op = b.first else { throw Malformed() }
        let frame: Frame
        switch op {
        case opOutput:
            guard b.count >= 13 else { throw Malformed() }
            frame = .output(channel: readBigEndian(b, 1), offset: readBigEndian(b, 5), payload: Data(b[13...]))
        case opInput:
            guard b.count >= 5 else { throw Malformed() }
            frame = .input(channel: readBigEndian(b, 1), payload: Data(b[5...]))
        default:
            throw Malformed()
        }
        guard frame.payload.count <= Proto.maxPayload else { throw Malformed() }
        return frame
    }

    public var payload: Data {
        switch self {
        case let .output(_, _, p), let .input(_, p): return p
        }
    }

    private static func readBigEndian<T: FixedWidthInteger>(_ b: [UInt8], _ at: Int) -> T {
        var v: T = 0
        for i in 0..<MemoryLayout<T>.size {
            v = v << 8 | T(b[at + i])
        }
        return v
    }
}

extension Data {
    mutating func appendBigEndian<T: FixedWidthInteger>(_ v: T) {
        Swift.withUnsafeBytes(of: v.bigEndian) { append(contentsOf: $0) }
    }
}
