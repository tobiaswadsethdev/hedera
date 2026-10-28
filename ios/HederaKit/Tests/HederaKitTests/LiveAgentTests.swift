// Against a real agent, opt-in:
//
//   hedera run --dev-loopback &
//   HEDERA_LIVE_PAIR_URL="$(hedera pair --no-qr | grep hedera://)" swift test --filter LiveAgent
//
// Each run uses up one pairing code.

import Foundation
import Testing
@testable import HederaKit

private let liveURL = ProcessInfo.processInfo.environment["HEDERA_LIVE_PAIR_URL"]

@MainActor
@Suite(.enabled(if: liveURL != nil, "set HEDERA_LIVE_PAIR_URL to run"))
struct LiveAgentTests {
    @Test func pairConnectAndType() async throws {
        let url = try PairingURL(liveURL!)

        // A wrong pin fails the TLS handshake, so the single-use code is never
        // sent: pairing with the right pin afterwards still works.
        var wrong = url
        wrong.fingerprint = "sha256:" + String(repeating: "A", count: 43)
        await #expect(throws: PairingError.pinMismatch) { try await Pairing.pair(wrong, deviceName: "HederaKit tests") }

        let record = try await Pairing.pair(url, deviceName: "HederaKit tests")
        #expect(record.token.hasPrefix("hdt_"))

        let client = MachineClient(record: record, clientInfo: ClientInfo(name: "HederaKit-tests", version: "0", platform: "macos"))
        client.resume()
        try await eventually("online") { client.state == .online }

        let s = try await client.createSession(SessionCreate(kind: "shell", argv: ["/bin/sh"], cols: 80, rows: 24))
        let sink = RecordingSink()
        let term = client.openTerminal(sessionID: s.id, size: TermSize(cols: 80, rows: 24), sink: sink)
        try await eventually("attached") { term.isAttached }
        term.send(Data("echo hedera-$((6*7))\r".utf8))
        try await eventually("output") { sink.string.contains("hedera-42") }

        // Drop the socket and type again: output continues without repeats.
        client.suspend()
        client.resume()
        try await eventually("reattached") { term.isAttached }
        term.send(Data("echo again-$((6*7))\r".utf8))
        try await eventually("more output") { sink.string.contains("again-42") }
        // Once: the command echoes as $((6*7)), so only its output says 42.
        #expect(sink.string.components(separatedBy: "hedera-42").count == 2)
        #expect(sink.resets == 0)

        try await client.closeSession(s.id, kill: true)
        try await eventually("exited") { client.session(s.id)?.isExited == true }
        try await client.removeSession(s.id)
        try await client.unpair()
        try await eventually("revoked") {
            if case .revoked = client.state { true } else { false }
        }

        // Connecting again with the revoked token is refused with 401, which
        // stops reconnecting rather than retrying.
        let stale = MachineClient(record: record, clientInfo: ClientInfo(name: "HederaKit-tests", version: "0", platform: "macos"))
        stale.resume()
        try await eventually("refused") {
            if case let .revoked(why) = stale.state { why == TransportError.unauthorized.localizedDescription } else { false }
        }
    }
}
