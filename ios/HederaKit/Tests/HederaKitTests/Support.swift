import Foundation
import Testing

/// protocol/fixtures at the repository root.
let fixtureDir = URL(fileURLWithPath: #filePath)
    .deletingLastPathComponent() // HederaKitTests
    .deletingLastPathComponent() // Tests
    .deletingLastPathComponent() // HederaKit
    .deletingLastPathComponent() // ios
    .deletingLastPathComponent() // repository root
    .appendingPathComponent("protocol/fixtures")

func loadFixture(_ name: String) throws -> [[String: Any]] {
    let data = try Data(contentsOf: fixtureDir.appendingPathComponent(name))
    return try #require(try JSONSerialization.jsonObject(with: data) as? [[String: Any]])
}

func jsonData(_ object: Any) throws -> Data {
    try JSONSerialization.data(withJSONObject: object, options: [.fragmentsAllowed])
}

/// Compares two JSON documents structurally. Unlike NSDictionary equality it
/// keeps booleans and numbers apart, so `true` never matches `1`.
func jsonEqual(_ a: Any, _ b: Any) -> Bool {
    switch (a, b) {
    case let (a as [String: Any], b as [String: Any]):
        return a.count == b.count && a.allSatisfy { k, v in b[k].map { jsonEqual(v, $0) } ?? false }
    case let (a as [Any], b as [Any]):
        return a.count == b.count && zip(a, b).allSatisfy(jsonEqual)
    case let (a as NSNumber, b as NSNumber):
        return isBool(a) == isBool(b) && a == b
    case let (a as String, b as String):
        return a == b
    case (is NSNull, is NSNull):
        return true
    default:
        return false
    }
}

private func isBool(_ n: NSNumber) -> Bool {
    CFGetTypeID(n) == CFBooleanGetTypeID()
}

func hex(_ s: String) -> Data {
    var d = Data()
    var i = s.startIndex
    while i < s.endIndex {
        let j = s.index(i, offsetBy: 2)
        d.append(UInt8(s[i..<j], radix: 16)!)
        i = j
    }
    return d
}
