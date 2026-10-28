import Foundation
import Testing
@testable import HederaKit

private func response(_ s: String) throws -> PinnedHTTP.Response {
    try PinnedHTTP.parse(Data(s.utf8))
}

@Test func parsesContentLength() throws {
    let r = try response("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 2\r\n\r\n{}trailing")
    #expect(r == PinnedHTTP.Response(status: 200, body: Data("{}".utf8)))
}

@Test func parsesBodyUntilClose() throws {
    let r = try response("HTTP/1.1 401 Unauthorized\r\nConnection: close\r\n\r\nunauthorized\n")
    #expect(r.status == 401)
    #expect(r.body == Data("unauthorized\n".utf8))
}

@Test func parsesChunked() throws {
    let r = try response("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n4\r\n{\"a\"\r\n3;ext=1\r\n:1}\r\n0\r\n\r\n")
    #expect(String(decoding: r.body, as: UTF8.self) == #"{"a":1}"#)
}

@Test(arguments: [
    "",
    "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n", // no blank line
    "SSH-2.0-OpenSSH\r\n\r\n",
    "HTTP/1.1 OK\r\n\r\n",
    "HTTP/1.1 200 OK\r\nContent-Length: 10\r\n\r\nshort",
    "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\nzz\r\n",
    "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n9\r\nshort",
])
func rejectsMalformed(raw: String) {
    #expect(throws: TransportError.self) { try PinnedHTTP.parse(Data(raw.utf8)) }
}
