// Hedera protocol v1 control messages. See protocol/spec.md at the repository
// root; the fixtures in protocol/fixtures are decoded by the tests so these
// types cannot drift from the Go agent's.
//
// Fields the agent always writes (even as null) are encoded explicitly, and
// fields it omits when empty are optionals, so encoding a decoded fixture
// reproduces it exactly.

import Foundation

public enum Proto {
    public static let version = 1
    public static let subprotocol = "hedera.v1"
    public static let maxPayload = 64 * 1024
    public static let maxTextMessage = 1 << 20
}

public enum CloseCode {
    public static let handshake = 4001
    public static let revoked = 4003
    public static let protocolViolation = 4008
    public static let writeTimeout = 4009
}

// MARK: - Errors

public struct ProtoError: Codable, Error, Equatable, Sendable, LocalizedError {
    public var code: String
    public var message: String

    public init(code: String, message: String) {
        self.code = code
        self.message = message
    }

    public var errorDescription: String? { message }

    public static let unsupportedVersion = "unsupported_version"
    public static let unknownType = "unknown_type"
    public static let invalidArgument = "invalid_argument"
    public static let notFound = "not_found"
    public static let sessionExited = "session_exited"
    public static let sessionRunning = "session_running"
    public static let limitExceeded = "limit_exceeded"
    public static let stale = "stale"
    public static let unavailable = "unavailable"
    public static let internalError = "internal"
}

// MARK: - Handshake

public struct VersionRange: Codable, Equatable, Sendable {
    public var min: Int
    public var max: Int
}

public struct ClientInfo: Codable, Equatable, Sendable {
    public var name: String
    public var version: String
    public var platform: String?

    public init(name: String, version: String, platform: String?) {
        self.name = name
        self.version = version
        self.platform = platform
    }
}

public struct AgentInfo: Codable, Equatable, Sendable {
    public var version: String
    public var os: String
    public var arch: String
    public var hostname: String
}

public struct Limits: Codable, Equatable, Sendable {
    public var maxSessions: Int
    public var scrollbackBytes: Int
    public var maxFrame: Int

    enum CodingKeys: String, CodingKey {
        case maxSessions = "max_sessions"
        case scrollbackBytes = "scrollback_bytes"
        case maxFrame = "max_frame"
    }
}

public struct HelloResult: Codable, Equatable, Sendable {
    public var `protocol`: Int
    public var machineID: String
    public var bootID: String
    public var agent: AgentInfo
    public var capabilities: [String]
    public var limits: Limits
    public var serverTime: String

    enum CodingKeys: String, CodingKey {
        case `protocol`, agent, capabilities, limits
        case machineID = "machine_id"
        case bootID = "boot_id"
        case serverTime = "server_time"
    }
}

// MARK: - Sessions

public struct TermSize: Codable, Equatable, Sendable {
    public var cols: Int
    public var rows: Int

    public init(cols: Int, rows: Int) {
        self.cols = cols
        self.rows = rows
    }
}

public struct Exit: Codable, Equatable, Sendable {
    public var code: Int?
    public var signal: String?
    public var at: String
    public var initiatedBy: String

    enum CodingKeys: String, CodingKey {
        case code, signal, at
        case initiatedBy = "initiated_by"
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(code, forKey: .code)
        try c.encode(signal, forKey: .signal)
        try c.encode(at, forKey: .at)
        try c.encode(initiatedBy, forKey: .initiatedBy)
    }
}

public struct NeedsInput: Codable, Equatable, Sendable {
    public var kind: String
    public var message: String
}

public struct ClaudeError: Codable, Equatable, Sendable {
    public var error: String
    public var errorDetails: String?

    enum CodingKeys: String, CodingKey {
        case error
        case errorDetails = "error_details"
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(error, forKey: .error)
        try c.encode(errorDetails, forKey: .errorDetails)
    }
}

public struct ClaudeState: Codable, Equatable, Sendable {
    public var status: String
    public var sessionID: String?
    public var turn: Int
    public var needsInput: NeedsInput?
    public var lastMessagePreview: String?
    public var lastError: ClaudeError?
    public var endReason: String?
    /// The agent can serve this session's transcript (section 6.2).
    public var transcript: Bool?

    enum CodingKeys: String, CodingKey {
        case status, turn, transcript
        case sessionID = "session_id"
        case needsInput = "needs_input"
        case lastMessagePreview = "last_message_preview"
        case lastError = "last_error"
        case endReason = "end_reason"
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(status, forKey: .status)
        try c.encode(sessionID, forKey: .sessionID)
        try c.encode(turn, forKey: .turn)
        try c.encode(needsInput, forKey: .needsInput)
        try c.encode(lastMessagePreview, forKey: .lastMessagePreview)
        try c.encode(lastError, forKey: .lastError)
        try c.encode(endReason, forKey: .endReason)
        if transcript == true { try c.encode(true, forKey: .transcript) }
    }
}

public enum SessionKind {
    public static let shell = "shell"
    public static let claude = "claude"
}

public enum SessionStatus {
    public static let running = "running"
    public static let starting = "starting"
    public static let working = "working"
    public static let idle = "idle"
    public static let needsInput = "needs_input"
    public static let failed = "failed"
    public static let exited = "exited"
}

public struct Session: Codable, Equatable, Identifiable, Sendable {
    public var id: String
    public var kind: String
    public var title: String
    public var cwd: String
    public var argv: [String]
    public var pid: Int
    public var createdAt: String
    public var size: TermSize
    public var status: String
    public var outputOffset: UInt64
    public var attached: Int
    public var claude: ClaudeState?
    public var exit: Exit?

    enum CodingKeys: String, CodingKey {
        case id, kind, title, cwd, argv, pid, size, status, attached, claude, exit
        case createdAt = "created_at"
        case outputOffset = "output_offset"
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(id, forKey: .id)
        try c.encode(kind, forKey: .kind)
        try c.encode(title, forKey: .title)
        try c.encode(cwd, forKey: .cwd)
        try c.encode(argv, forKey: .argv)
        try c.encode(pid, forKey: .pid)
        try c.encode(createdAt, forKey: .createdAt)
        try c.encode(size, forKey: .size)
        try c.encode(status, forKey: .status)
        try c.encode(outputOffset, forKey: .outputOffset)
        try c.encode(attached, forKey: .attached)
        try c.encode(claude, forKey: .claude)
        try c.encode(exit, forKey: .exit)
    }

    public var isExited: Bool { exit != nil }
}

public struct Replay: Codable, Equatable, Sendable {
    public var from: UInt64
    public var to: UInt64
    public var truncated: Bool
}

public struct Resumable: Codable, Equatable, Sendable, Identifiable {
    public var claudeSessionID: String
    public var cwd: String
    public var title: String
    public var lastSeenAt: String

    public var id: String { claudeSessionID }

    enum CodingKeys: String, CodingKey {
        case cwd, title
        case claudeSessionID = "claude_session_id"
        case lastSeenAt = "last_seen_at"
    }
}

// MARK: - Unsolicited messages

public struct Event: Codable, Equatable, Sendable {
    public var t = "event"
    public var seq: UInt64
    public var ts: String
    public var type: String
    public var session: Session?
    public var sessionID: String?
    public var reason: String?
    public var cols: Int?
    public var rows: Int?
    public var turn: Int?
    public var preview: String?
    public var kind: String?
    public var message: String?
    public var error: String?
    public var errorDetails: String?

    enum CodingKeys: String, CodingKey {
        case t, seq, ts, type, session, reason, cols, rows, turn, preview, kind, message, error
        case sessionID = "session_id"
        case errorDetails = "error_details"
    }

    public static let sessionCreated = "session.created"
    public static let sessionUpdated = "session.updated"
    public static let sessionResized = "session.resized"
    public static let sessionExited = "session.exited"
    public static let sessionRemoved = "session.removed"
    public static let claudeTurnFinished = "claude.turn_finished"
    public static let claudeNeedsInput = "claude.needs_input"
    public static let claudeFailed = "claude.failed"
    public static let claudeEnded = "claude.ended"
}

public struct Lagged: Codable, Equatable, Sendable {
    public var t = "lagged"
    public var channel: UInt32
    public var sessionID: String
    public var offset: UInt64

    enum CodingKeys: String, CodingKey {
        case t, channel, offset
        case sessionID = "session_id"
    }
}

public struct Detached: Codable, Equatable, Sendable {
    public var t = "detached"
    public var channel: UInt32
    public var sessionID: String
    public var reason: String

    enum CodingKeys: String, CodingKey {
        case t, channel, reason
        case sessionID = "session_id"
    }
}

public struct EventsLagged: Codable, Equatable, Sendable {
    public var t = "events.lagged"
    public var seq: UInt64
}
