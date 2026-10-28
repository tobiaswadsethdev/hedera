import Foundation
import Security
import Testing
@testable import HederaKit

/// protocol/fixtures/pinning.json, checked by the Go tests too.
@Test func pinningFixtures() throws {
    let fixtures = try loadFixture("pinning.json")
    #expect(!fixtures.isEmpty)
    for f in fixtures {
        let der = try #require(Data(base64Encoded: f["cert_der_base64"] as! String))
        let spki = try #require(Pinning.spki(fromCertificate: der), "\(f["name"]!)")
        #expect(spki.base64EncodedString() == f["spki_der_base64"] as? String, "\(f["name"]!)")
        #expect(Pinning.fingerprint(certificateDER: der) == f["fp"] as? String, "\(f["name"]!)")

        // The same certificate, through the Security framework path the
        // session delegate uses.
        let cert = try #require(SecCertificateCreateWithData(nil, der as CFData))
        var trust: SecTrust?
        #expect(SecTrustCreateWithCertificates(cert, SecPolicyCreateBasicX509(), &trust) == errSecSuccess)
        #expect(Pinning.matches(trust!, pin: f["fp"] as! String))
        #expect(!Pinning.matches(trust!, pin: "sha256:AAAA"))
    }
}

@Test func spkiRejectsGarbage() {
    #expect(Pinning.spki(fromCertificate: Data()) == nil)
    #expect(Pinning.spki(fromCertificate: Data([0x30, 0x82, 0xFF])) == nil)
    #expect(Pinning.spki(fromCertificate: Data([0x30, 0x03, 0x02, 0x01, 0x01])) == nil)
}

/// protocol/fixtures/pairing.json, checked by the Go tests too.
@Test func pairingFixtures() throws {
    for f in try loadFixture("pairing.json") {
        let name = f["name"] as! String
        let raw = f["url"] as! String
        guard f["valid"] as! Bool else {
            #expect(throws: PairingURL.ParseError.self, "\(name)") { try PairingURL(raw) }
            continue
        }
        let p = try PairingURL(raw)
        #expect(p.machineID == f["machine_id"] as? String, "\(name)")
        #expect(p.name == f["name_param"] as? String, "\(name)")
        #expect(p.hosts == f["hosts"] as? [String], "\(name)")
        #expect(p.port == f["port"] as? String, "\(name)")
        #expect(p.fingerprint == f["fp"] as? String, "\(name)")
        #expect(p.code == f["code"] as? String, "\(name)")
    }
}

@Test func ipv6HostsAreBracketed() {
    #expect("fd7a:115c:a1e0::1".urlHost == "[fd7a:115c:a1e0::1]")
    #expect("100.64.0.1".urlHost == "100.64.0.1")
}

@Test func recordNeverPrintsItsToken() {
    let r = MachineRecord(machineID: "m_1", name: "studio", hosts: ["100.64.0.1"], port: "7373",
                          fingerprint: "sha256:x", pairingID: "p_1", token: "hdt_supersecret")
    var dumped = ""
    dump(r, to: &dumped)
    for s in ["\(r)", String(describing: r), String(reflecting: r), dumped] {
        #expect(!s.contains("hdt_supersecret"), "\(s)")
    }
}

@Test func keychainRoundTrip() throws {
    let store = KeychainMachineStore(service: "hedera.tests.\(UUID().uuidString)")
    let r = MachineRecord(machineID: "m_1", name: "studio", hosts: ["100.64.0.1"], port: "7373",
                          fingerprint: "sha256:x", pairingID: "p_1", token: "hdt_t")
    do {
        try store.save(r)
    } catch let e as KeychainError where e.status == errSecMissingEntitlement {
        // `swift test` binaries aren't signed with a keychain entitlement; the
        // app's test run on the simulator covers this.
        return
    }
    defer { try? store.delete(machineID: "m_1") }
    #expect(try store.loadAll().map(\.token) == ["hdt_t"])
    var renamed = r
    renamed.name = "studio 2"
    try store.save(renamed)
    #expect(try store.loadAll().map(\.name) == ["studio 2"])
    try store.delete(machineID: "m_1")
    #expect(try store.loadAll().isEmpty)
}
