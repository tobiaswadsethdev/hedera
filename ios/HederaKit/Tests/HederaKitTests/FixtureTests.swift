// Decodes every golden fixture in protocol/fixtures into its Swift type and
// re-encodes it, mirroring agent/internal/proto/proto_test.go. A field the
// Swift types drop or add makes these fail.

import Foundation
import Testing
@testable import HederaKit

private typealias Roundtrip = (Data) throws -> Data
private typealias Reply = (Data, UInt64) throws -> Data

private func request<R: AgentRequest>(_: R.Type) -> Reply {
    { data, id in try Wire.encodeRequest(try JSONDecoder().decode(R.self, from: data), id: id) }
}

private func result<R: Codable>(_: R.Type) -> Reply {
    { data, re in try JSONEncoder().encode(ResultEnvelope(re: re, result: try JSONDecoder().decode(R.self, from: data))) }
}

private func plain<T: Codable>(_: T.Type) -> Roundtrip {
    { try JSONEncoder().encode(try JSONDecoder().decode(T.self, from: $0)) }
}

nonisolated(unsafe) private let requestTypes: [String: Reply] = [
    Hello.type: request(Hello.self),
    SessionList.type: request(SessionList.self),
    SessionCreate.type: request(SessionCreate.self),
    SessionAttach.type: request(SessionAttach.self),
    SessionDetach.type: request(SessionDetach.self),
    SessionInput.type: request(SessionInput.self),
    SessionResize.type: request(SessionResize.self),
    SessionClose.type: request(SessionClose.self),
    SessionRemove.type: request(SessionRemove.self),
    EventsSubscribe.type: request(EventsSubscribe.self),
    EventsUnsubscribe.type: request(EventsUnsubscribe.self),
    PushConfigure.type: request(PushConfigure.self),
    PushStatusRequest.type: request(PushStatusRequest.self),
    PushRemove.type: request(PushRemove.self),
    PairingUnpair.type: request(PairingUnpair.self),
    SessionResumable.type: request(SessionResumable.self),
    SessionResume.type: request(SessionResume.self),
    SessionForget.type: request(SessionForget.self),
]

nonisolated(unsafe) private let resultTypes: [String: Reply] = [
    Hello.type: result(Hello.Result.self),
    SessionList.type: result(SessionList.Result.self),
    SessionCreate.type: result(SessionCreate.Result.self),
    SessionAttach.type: result(SessionAttach.Result.self),
    SessionResize.type: result(SessionResize.Result.self),
    EventsSubscribe.type: result(EventsSubscribe.Result.self),
    PushConfigure.type: result(PushConfigure.Result.self),
    PushStatusRequest.type: result(PushStatusRequest.Result.self),
    SessionResumable.type: result(SessionResumable.Result.self),
]

nonisolated(unsafe) private let unsolicitedTypes: [String: Roundtrip] = [
    "event": plain(Event.self),
    "lagged": plain(Lagged.self),
    "detached": plain(Detached.self),
    "events.lagged": plain(EventsLagged.self),
]

@Test func messageFixtures() throws {
    let fixtures = try loadFixture("messages.json")
    #expect(!fixtures.isEmpty)
    for f in fixtures {
        let name = f["name"] as! String
        let message = f["message"] as! [String: Any]
        let data = try jsonData(message)
        let t = message["t"] as! String
        let got: Data
        if f["direction"] as? String == "client_to_agent" {
            let encode = try #require(requestTypes[t], "no Swift type for request \(t)")
            got = try encode(data, (message["id"] as! NSNumber).uint64Value)
        } else if t == "res", message["ok"] as? Bool == false {
            got = try JSONEncoder().encode(try JSONDecoder().decode(ResponseHeader.self, from: data))
        } else if t == "res" {
            let to = try #require(f["response_to"] as? String, "\(name): no response_to")
            let encode = try #require(resultTypes[to], "no Swift type for result of \(to)")
            got = try encode(data, (message["re"] as! NSNumber).uint64Value)
        } else {
            let encode = try #require(unsolicitedTypes[t], "no Swift type for \(t)")
            got = try encode(data)
        }
        let back = try JSONSerialization.jsonObject(with: got)
        #expect(jsonEqual(message, back), "\(name): re-encoded as \(String(decoding: got, as: UTF8.self))")
    }
}

@Test func inboundDispatch() throws {
    for f in try loadFixture("messages.json") where f["direction"] as? String == "agent_to_client" {
        let message = f["message"] as! [String: Any]
        let inbound = try Wire.decodeInbound(try jsonData(message))
        switch (message["t"] as! String, inbound) {
        case ("res", .response), ("event", .event), ("lagged", .lagged), ("detached", .detached),
             ("events.lagged", .eventsLagged):
            break
        case let (t, got):
            Issue.record("\(t) decoded as \(got)")
        }
    }
    if case .unknown("future.thing") = try Wire.decodeInbound(Data(#"{"t":"future.thing","x":1}"#.utf8)) {} else {
        Issue.record("unknown message types must be passed through")
    }
}

@Test func binaryFixtures() throws {
    let fixtures = try loadFixture("binary.json")
    #expect(!fixtures.isEmpty)
    for f in fixtures {
        let frame = hex(f["frame_hex"] as! String)
        let payload = hex(f["payload_hex"] as! String)
        let channel = (f["channel"] as! NSNumber).uint32Value
        let want: Frame = switch f["op"] as! String {
        case "output": .output(channel: channel, offset: (f["offset"] as! NSNumber).uint64Value, payload: payload)
        default: .input(channel: channel, payload: payload)
        }
        #expect(try Frame.decode(frame) == want, "\(f["name"]!)")
        #expect(want.encode() == frame, "\(f["name"]!)")
    }
}

@Test func malformedFrames() {
    let bad: [Data] = [
        Data(),
        Data([0x03, 0, 0, 0, 1]),
        Data([0x01, 0, 0, 0, 1, 0, 0, 0]),
        Data([0x02, 0, 0]),
        Data([0x02, 0, 0, 0, 1]) + Data(count: Proto.maxPayload + 1),
    ]
    for b in bad {
        #expect(throws: Frame.Malformed.self) { try Frame.decode(b) }
    }
}
