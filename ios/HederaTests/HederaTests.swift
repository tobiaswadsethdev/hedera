import Foundation
import HederaKit
import Testing
@testable import Hedera

private func record(_ id: String = "m_1", token: String = "hdt_t") -> MachineRecord {
    MachineRecord(machineID: id, name: "studio", hosts: ["127.0.0.1"], port: "7373",
                  fingerprint: "sha256:x", pairingID: "p_1", token: token)
}

/// Runs in the app on the simulator, where the Keychain is available (unlike
/// `swift test` on the Mac).
@Test func keychainStoresAndReplacesRecords() throws {
    let store = KeychainMachineStore(service: "hedera.tests.\(UUID().uuidString)")
    defer {
        try? store.delete(machineID: "m_1")
        try? store.delete(machineID: "m_2")
    }
    try store.save(record())
    try store.save(record("m_2"))
    try store.save(record(token: "hdt_new"))
    let all = try store.loadAll()
    #expect(all.map(\.machineID).sorted() == ["m_1", "m_2"])
    #expect(all.first { $0.machineID == "m_1" }?.token == "hdt_new")
    try store.delete(machineID: "m_1")
    #expect(try store.loadAll().map(\.machineID) == ["m_2"])
}

@Test func tokensStayOutOfUserDefaults() throws {
    let store = KeychainMachineStore(service: "hedera.tests.\(UUID().uuidString)")
    defer { try? store.delete(machineID: "m_1") }
    try store.save(record(token: "hdt_canary_value"))
    let defaults = UserDefaults.standard.dictionaryRepresentation().description
    #expect(!defaults.contains("hdt_canary_value"))
}

@MainActor
@Test func pairingTheSameMachineAgainReplacesIt() throws {
    let model = AppModel(store: InMemoryMachineStore())
    try model.add(record())
    try model.add(record(token: "hdt_2"))
    #expect(model.machines.count == 1)
    #expect(model.machines.first?.record.token == "hdt_2")
}

@MainActor
@Test func removingForgetsTheMachine() async throws {
    let store = InMemoryMachineStore()
    let model = AppModel(store: store)
    let m = try model.add(record())
    let revoked = try await model.remove(m)
    #expect(!revoked) // never connected, so nothing to revoke
    #expect(model.machines.isEmpty)
    #expect(try store.loadAll().isEmpty)
}

@Test(arguments: [
    ("", []),
    ("htop", ["htop"]),
    ("claude --resume abc", ["claude", "--resume", "abc"]),
    ("git commit -m 'two words'", ["git", "commit", "-m", "two words"]),
    (#"echo "a \"b\"" c\ d"#, ["echo", #"a "b""#, "c d"]),
    ("  spaced   out ", ["spaced", "out"]),
    ("empty ''", ["empty", ""]),
])
func shellWords(line: String, words: [String]) {
    #expect(ShellWords.split(line) == words)
}
