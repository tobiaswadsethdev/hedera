// Certificate pinning (protocol/spec.md section 1.1). The agent's certificate
// is self-signed; we accept a connection if and only if the leaf's
// SubjectPublicKeyInfo hashes to the fingerprint from the pairing QR code.
// The system trust store and hostnames play no part.

import CryptoKit
import Foundation
import Security

public enum Pinning {
    /// `"sha256:" + base64url_nopad(SHA-256(spki))`
    public static func fingerprint(spki: Data) -> String {
        let digest = Data(SHA256.hash(data: spki))
        let b64 = digest.base64EncodedString()
            .replacingOccurrences(of: "+", with: "-")
            .replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "=", with: "")
        return "sha256:" + b64
    }

    /// The fingerprint of a DER certificate, or nil if it can't be parsed.
    public static func fingerprint(certificateDER: Data) -> String? {
        spki(fromCertificate: certificateDER).map(fingerprint(spki:))
    }

    /// Whether the leaf certificate of `trust` matches `pin`. Compares in
    /// constant time, though the pin is not a secret.
    public static func matches(_ trust: SecTrust, pin: String) -> Bool {
        guard let chain = SecTrustCopyCertificateChain(trust) as? [SecCertificate],
              let leaf = chain.first,
              let got = fingerprint(certificateDER: SecCertificateCopyData(leaf) as Data)
        else { return false }
        return constantTimeEqual(Array(got.utf8), Array(pin.utf8))
    }

    static func constantTimeEqual(_ a: [UInt8], _ b: [UInt8]) -> Bool {
        guard a.count == b.count else { return false }
        var diff: UInt8 = 0
        for i in a.indices { diff |= a[i] ^ b[i] }
        return diff == 0
    }

    /// Extracts the DER SubjectPublicKeyInfo from an X.509 certificate:
    ///
    ///     Certificate ::= SEQUENCE { tbsCertificate, signatureAlgorithm, signature }
    ///     TBSCertificate ::= SEQUENCE { [0] version OPTIONAL, serialNumber,
    ///         signature, issuer, validity, subject, subjectPublicKeyInfo, … }
    static func spki(fromCertificate der: Data) -> Data? {
        let b = [UInt8](der)
        guard let cert = DER.element(b, at: 0), cert.tag == 0x30,
              let tbs = DER.element(b, at: cert.contentStart), tbs.tag == 0x30
        else { return nil }
        var pos = tbs.contentStart
        var el = DER.element(b, at: pos)
        if el?.tag == 0xA0 { // explicit version
            pos = el!.end
            el = DER.element(b, at: pos)
        }
        // serialNumber, signature, issuer, validity, subject
        for _ in 0..<5 {
            guard let e = el, e.end <= tbs.end else { return nil }
            pos = e.end
            el = DER.element(b, at: pos)
        }
        guard let spki = el, spki.tag == 0x30, spki.end <= tbs.end else { return nil }
        return Data(b[spki.start..<spki.end])
    }
}

/// Just enough DER to walk a certificate.
enum DER {
    struct Element {
        var tag: UInt8
        var start: Int
        var contentStart: Int
        var end: Int
    }

    static func element(_ b: [UInt8], at start: Int) -> Element? {
        guard start + 2 <= b.count else { return nil }
        let tag = b[start]
        guard tag & 0x1F != 0x1F else { return nil } // multi-byte tags don't occur here
        var pos = start + 1
        var length = Int(b[pos])
        pos += 1
        if length & 0x80 != 0 {
            let n = length & 0x7F
            guard n > 0, n <= 4, pos + n <= b.count else { return nil }
            length = 0
            for _ in 0..<n {
                length = length << 8 | Int(b[pos])
                pos += 1
            }
        }
        guard pos + length <= b.count else { return nil }
        return Element(tag: tag, start: start, contentStart: pos, end: pos + length)
    }
}
