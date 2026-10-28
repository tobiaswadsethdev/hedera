// Transcript messages (protocol/spec.md section 6.2): a structured view of a
// Claude session's conversation, read by the agent from Claude Code's JSONL
// transcript.

import Foundation

/// Any JSON value, for tool inputs whose shape depends on the tool.
public enum JSONValue: Codable, Equatable, Sendable {
    case null
    case bool(Bool)
    case number(Double)
    case string(String)
    case array([JSONValue])
    case object([String: JSONValue])

    public init(from decoder: Decoder) throws {
        let c = try decoder.singleValueContainer()
        if c.decodeNil() {
            self = .null
        } else if let b = try? c.decode(Bool.self) {
            self = .bool(b)
        } else if let n = try? c.decode(Double.self) {
            self = .number(n)
        } else if let s = try? c.decode(String.self) {
            self = .string(s)
        } else if let a = try? c.decode([JSONValue].self) {
            self = .array(a)
        } else {
            self = .object(try c.decode([String: JSONValue].self))
        }
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.singleValueContainer()
        switch self {
        case .null: try c.encodeNil()
        case let .bool(b): try c.encode(b)
        case let .number(n): try c.encode(n)
        case let .string(s): try c.encode(s)
        case let .array(a): try c.encode(a)
        case let .object(o): try c.encode(o)
        }
    }

    public subscript(key: String) -> JSONValue? {
        if case let .object(o) = self { o[key] } else { nil }
    }

    public var string: String? {
        if case let .string(s) = self { s } else { nil }
    }

    public var bool: Bool? {
        if case let .bool(b) = self { b } else { nil }
    }
}

public struct TranscriptBlock: Codable, Equatable, Sendable {
    public var type: String
    /// text, thinking and tool_result.
    public var text: String?
    /// tool_use.
    public var id: String?
    public var name: String?
    public var input: JSONValue?
    /// tool_result.
    public var toolUseID: String?
    public var isError: Bool?
    public var images: Int?
    public var truncated: Bool?

    enum CodingKeys: String, CodingKey {
        case type, text, id, name, input, images, truncated
        case toolUseID = "tool_use_id"
        case isError = "is_error"
    }

    public init(type: String, text: String? = nil, id: String? = nil, name: String? = nil, input: JSONValue? = nil,
                toolUseID: String? = nil, isError: Bool? = nil, images: Int? = nil, truncated: Bool? = nil) {
        self.type = type
        self.text = text
        self.id = id
        self.name = name
        self.input = input
        self.toolUseID = toolUseID
        self.isError = isError
        self.images = images
        self.truncated = truncated
    }
}

public struct TranscriptEntry: Codable, Equatable, Identifiable, Sendable {
    public var seq: UInt64
    public var uuid: String?
    public var ts: String?
    public var kind: String
    public var blocks: [TranscriptBlock]?
    public var durationMS: Int64?
    public var reason: String?
    public var level: String?

    public var id: UInt64 { seq }

    enum CodingKeys: String, CodingKey {
        case seq, uuid, ts, kind, blocks, reason, level
        case durationMS = "duration_ms"
    }

    public init(seq: UInt64, uuid: String? = nil, ts: String? = nil, kind: String, blocks: [TranscriptBlock]? = nil,
                durationMS: Int64? = nil, reason: String? = nil, level: String? = nil) {
        self.seq = seq
        self.uuid = uuid
        self.ts = ts
        self.kind = kind
        self.blocks = blocks
        self.durationMS = durationMS
        self.reason = reason
        self.level = level
    }

    public enum Kind {
        public static let prompt = "prompt"
        public static let assistant = "assistant"
        public static let toolResult = "tool_result"
        public static let turnEnd = "turn_end"
        public static let divider = "divider"
        public static let notice = "notice"
    }
}

public struct TranscriptSubscribe: AgentRequest {
    public struct Result: Codable, Equatable, Sendable {
        public var seq: UInt64
        public var firstSeq: UInt64
        public var reset: Bool

        enum CodingKeys: String, CodingKey {
            case seq, reset
            case firstSeq = "first_seq"
        }
    }
    public static let type = "transcript.subscribe"
    public var sessionID: String
    public var since: UInt64?
    public var tail: Int?

    enum CodingKeys: String, CodingKey {
        case since, tail
        case sessionID = "session_id"
    }

    public init(sessionID: String, since: UInt64? = nil, tail: Int? = nil) {
        self.sessionID = sessionID
        self.since = since
        self.tail = tail
    }
}

public struct TranscriptUnsubscribe: AgentRequest {
    public typealias Result = EmptyResult
    public static let type = "transcript.unsubscribe"
    public var sessionID: String

    enum CodingKeys: String, CodingKey {
        case sessionID = "session_id"
    }

    public init(sessionID: String) { self.sessionID = sessionID }
}

public struct TranscriptRead: AgentRequest {
    public struct Result: Codable, Equatable, Sendable {
        public var entries: [TranscriptEntry]
    }
    public static let type = "transcript.read"
    public var sessionID: String
    public var before: UInt64
    public var limit: Int?

    enum CodingKeys: String, CodingKey {
        case before, limit
        case sessionID = "session_id"
    }

    public init(sessionID: String, before: UInt64, limit: Int? = nil) {
        self.sessionID = sessionID
        self.before = before
        self.limit = limit
    }
}

/// New entries for a transcript subscription.
public struct TranscriptMessage: Codable, Equatable, Sendable {
    public var t = "transcript"
    public var sessionID: String
    public var entries: [TranscriptEntry]

    enum CodingKeys: String, CodingKey {
        case t, entries
        case sessionID = "session_id"
    }
}
