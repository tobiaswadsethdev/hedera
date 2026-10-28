import Foundation

/// Tracks how far a terminal has rendered a session's output stream, so a
/// re-attach resumes exactly there and overlapping frames are trimmed. This is
/// what keeps reconnects free of gaps and duplicates.
public struct OutputCursor: Equatable, Sendable {
    /// The offset of the next byte to render; nil until the first attach.
    public private(set) var next: UInt64?

    public init(next: UInt64? = nil) {
        self.next = next
    }

    /// The offset to pass as `from_offset` when (re-)attaching.
    public var resumeOffset: UInt64? { next }

    /// Applies an attach response. Returns true if the terminal must be reset
    /// before rendering the replay (`truncated`, or a replay that doesn't
    /// continue where we left off).
    public mutating func attached(replay: Replay) -> Bool {
        defer { next = replay.from }
        guard let next else { return replay.truncated }
        return replay.truncated || replay.from != next
    }

    /// Returns the part of an OUTPUT frame that hasn't been rendered yet, or
    /// nil if all of it has.
    public mutating func accept(offset: UInt64, payload: Data) -> Data? {
        let end = offset + UInt64(payload.count)
        guard let next else {
            self.next = end
            return payload.isEmpty ? nil : payload
        }
        if end <= next { return nil }
        // offset > next would be a gap, which the agent never produces;
        // render what we have rather than stall.
        let skip = offset < next ? Int(next - offset) : 0
        self.next = end
        return payload.dropFirst(skip).isEmpty ? nil : Data(payload.dropFirst(skip))
    }
}

/// Exponential backoff with full jitter: attempt n waits a random time in
/// [0, min(cap, base·2ⁿ)], but never less than `floor`.
public struct Backoff: Sendable {
    public var base: Duration = .milliseconds(500)
    public var cap: Duration = .seconds(30)
    public var floor: Duration = .milliseconds(250)

    public init() {}

    public init(base: Duration, cap: Duration, floor: Duration) {
        self.base = base
        self.cap = cap
        self.floor = floor
    }

    public func delay(attempt: Int, random: Double = .random(in: 0...1)) -> Duration {
        let ceiling = min(cap, base * Int(1 << min(attempt, 20)))
        return max(floor, ceiling * random)
    }
}
