// Requests (client → agent) and their results. A request encodes as
// {"t": type, "id": id, ...params}; a success response as
// {"t": "res", "re": id, "ok": true, ...result}.

import Foundation

public protocol AgentRequest: Codable, Sendable {
    associatedtype Result: Decodable & Sendable
    static var type: String { get }
}

/// The result of requests that return nothing beyond `ok`.
public struct EmptyResult: Codable, Equatable, Sendable {
    public init() {}
}

public struct Hello: AgentRequest {
    public typealias Result = HelloResult
    public static let type = "hello"
    public var `protocol`: VersionRange
    public var client: ClientInfo

    public init(client: ClientInfo) {
        self.protocol = VersionRange(min: Proto.version, max: Proto.version)
        self.client = client
    }
}

public struct SessionList: AgentRequest {
    public struct Result: Codable, Equatable, Sendable {
        public var sessions: [Session]
    }
    public static let type = "session.list"
    public init() {}
}

public struct SessionCreate: AgentRequest {
    public struct Result: Codable, Equatable, Sendable {
        public var session: Session
    }
    public static let type = "session.create"
    public var kind: String
    public var cwd: String?
    public var argv: [String]?
    public var env: [String: String]?
    public var cols: Int?
    public var rows: Int?
    public var title: String?

    public init(kind: String, cwd: String? = nil, argv: [String]? = nil, env: [String: String]? = nil,
                cols: Int? = nil, rows: Int? = nil, title: String? = nil) {
        self.kind = kind
        self.cwd = cwd
        self.argv = argv
        self.env = env
        self.cols = cols
        self.rows = rows
        self.title = title
    }
}

public struct SessionAttach: AgentRequest {
    public struct Result: Codable, Equatable, Sendable {
        public var channel: UInt32
        public var session: Session
        public var replay: Replay
    }
    public static let type = "session.attach"
    public var sessionID: String
    public var fromOffset: UInt64?
    public var cols: Int?
    public var rows: Int?

    enum CodingKeys: String, CodingKey {
        case cols, rows
        case sessionID = "session_id"
        case fromOffset = "from_offset"
    }

    public init(sessionID: String, fromOffset: UInt64?, cols: Int? = nil, rows: Int? = nil) {
        self.sessionID = sessionID
        self.fromOffset = fromOffset
        self.cols = cols
        self.rows = rows
    }
}

public struct SessionDetach: AgentRequest {
    public typealias Result = EmptyResult
    public static let type = "session.detach"
    public var channel: UInt32
    public init(channel: UInt32) { self.channel = channel }
}

public struct SessionInput: AgentRequest {
    public typealias Result = EmptyResult
    public static let type = "session.input"
    public var sessionID: String
    public var data: String
    public var submit: Bool?
    public var ifTurn: Int?

    enum CodingKeys: String, CodingKey {
        case data, submit
        case sessionID = "session_id"
        case ifTurn = "if_turn"
    }

    public init(sessionID: String, data: String, submit: Bool? = nil, ifTurn: Int? = nil) {
        self.sessionID = sessionID
        self.data = data
        self.submit = submit
        self.ifTurn = ifTurn
    }
}

public struct SessionResize: AgentRequest {
    public typealias Result = EmptyResult
    public static let type = "session.resize"
    public var sessionID: String
    public var cols: Int
    public var rows: Int

    enum CodingKeys: String, CodingKey {
        case cols, rows
        case sessionID = "session_id"
    }

    public init(sessionID: String, cols: Int, rows: Int) {
        self.sessionID = sessionID
        self.cols = cols
        self.rows = rows
    }
}

public struct SessionClose: AgentRequest {
    public typealias Result = EmptyResult
    public static let type = "session.close"
    public var sessionID: String
    public var mode: String?

    enum CodingKeys: String, CodingKey {
        case mode
        case sessionID = "session_id"
    }

    public init(sessionID: String, mode: String? = nil) {
        self.sessionID = sessionID
        self.mode = mode
    }
}

public struct SessionRemove: AgentRequest {
    public typealias Result = EmptyResult
    public static let type = "session.remove"
    public var sessionID: String

    enum CodingKeys: String, CodingKey {
        case sessionID = "session_id"
    }

    public init(sessionID: String) { self.sessionID = sessionID }
}

public struct EventsSubscribe: AgentRequest {
    public struct Result: Codable, Equatable, Sendable {
        public var seq: UInt64
        public var reset: Bool
    }
    public static let type = "events.subscribe"
    public var sinceSeq: UInt64?

    enum CodingKeys: String, CodingKey {
        case sinceSeq = "since_seq"
    }

    public init(sinceSeq: UInt64?) { self.sinceSeq = sinceSeq }
}

public struct EventsUnsubscribe: AgentRequest {
    public typealias Result = EmptyResult
    public static let type = "events.unsubscribe"
    public init() {}
}

public struct PushStatus: Codable, Equatable, Sendable {
    public var configured: Bool
    public var radixURL: String?
    public var label: String?
    public var prefs: [String: Bool]?
    public var broken: String?
    public var lastSuccessAt: String?
    public var lastError: String?
    /// Notification text is encrypted end to end (protocol/spec.md section 7.4).
    public var encrypted: Bool?
    public var queued: Int

    enum CodingKeys: String, CodingKey {
        case configured, label, prefs, broken, encrypted, queued
        case radixURL = "radix_url"
        case lastSuccessAt = "last_success_at"
        case lastError = "last_error"
    }
}

public struct PushConfigure: AgentRequest {
    public typealias Result = PushStatus
    public static let type = "push.configure"
    public var radixURL: String
    public var pushToken: String
    /// 32 random bytes, base64url without padding; encrypts notification text.
    public var pushKey: String?
    public var prefs: [String: Bool]?

    enum CodingKeys: String, CodingKey {
        case prefs
        case radixURL = "radix_url"
        case pushToken = "push_token"
        case pushKey = "push_key"
    }

    public init(radixURL: String, pushToken: String, pushKey: String? = nil, prefs: [String: Bool]? = nil) {
        self.radixURL = radixURL
        self.pushToken = pushToken
        self.pushKey = pushKey
        self.prefs = prefs
    }
}

public struct PushStatusRequest: AgentRequest {
    public typealias Result = PushStatus
    public static let type = "push.status"
    public init() {}
}

public struct PushRemove: AgentRequest {
    public typealias Result = EmptyResult
    public static let type = "push.remove"
    public init() {}
}

public struct PairingUnpair: AgentRequest {
    public typealias Result = EmptyResult
    public static let type = "pairing.unpair"
    public init() {}
}

public struct SessionResumable: AgentRequest {
    public struct Result: Codable, Equatable, Sendable {
        public var resumable: [Resumable]
    }
    public static let type = "session.resumable"
    public init() {}
}

public struct SessionResume: AgentRequest {
    public typealias Result = SessionCreate.Result
    public static let type = "session.resume"
    public var claudeSessionID: String
    public var cols: Int?
    public var rows: Int?

    enum CodingKeys: String, CodingKey {
        case cols, rows
        case claudeSessionID = "claude_session_id"
    }

    public init(claudeSessionID: String, cols: Int? = nil, rows: Int? = nil) {
        self.claudeSessionID = claudeSessionID
        self.cols = cols
        self.rows = rows
    }
}

public struct SessionForget: AgentRequest {
    public typealias Result = EmptyResult
    public static let type = "session.forget"
    public var claudeSessionID: String

    enum CodingKeys: String, CodingKey {
        case claudeSessionID = "claude_session_id"
    }

    public init(claudeSessionID: String) { self.claudeSessionID = claudeSessionID }
}
