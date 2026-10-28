// TLS for every connection to an agent, on Network.framework rather than
// URLSession. URLSession applies App Transport Security, which re-checks a
// certificate our pinning accepted against the system trust store and rejects
// the agent's self-signed one on any address but loopback. Network.framework
// leaves the decision to the verify block below, so the pin is the only check.

import Foundation
import Network
import os

/// Remembers what the verify block decided, so a failed connection can be
/// reported as a pin mismatch rather than a generic TLS error.
final class PinCheck: Sendable {
    enum Outcome { case pending, matched, mismatched }
    private let state = OSAllocatedUnfairLock(initialState: Outcome.pending)

    var outcome: Outcome { state.withLock { $0 } }

    fileprivate func record(_ matched: Bool) {
        state.withLock { $0 = matched ? .matched : .mismatched }
    }
}

enum PinnedTLS {
    /// TCP and TLS 1.3 parameters that accept the server only if its leaf
    /// certificate's key matches `pin`. Nothing is sent over the connection
    /// before the verify block has accepted the key.
    static func parameters(pin: String, check: PinCheck) -> NWParameters {
        let tls = NWProtocolTLS.Options()
        let sec = tls.securityProtocolOptions
        sec_protocol_options_set_min_tls_protocol_version(sec, .TLSv13)
        sec_protocol_options_set_verify_block(sec, { _, trust, complete in
            let matched = Pinning.matches(sec_trust_copy_ref(trust).takeRetainedValue(), pin: pin)
            check.record(matched)
            complete(matched)
        }, DispatchQueue.global(qos: .userInitiated))

        let tcp = NWProtocolTCP.Options()
        tcp.connectionTimeout = 10
        tcp.enableKeepalive = true
        tcp.keepaliveIdle = 30
        return NWParameters(tls: tls, tcp: tcp)
    }
}
