import Foundation
import HederaKit
import Testing
@testable import Hedera

private func text(_ s: String) -> TranscriptBlock { TranscriptBlock(type: "text", text: s) }

@Test func foldJoinsToolCallsWithTheirResults() {
    let entries = [
        TranscriptEntry(seq: 1, kind: "prompt", blocks: [text("run it"), TranscriptBlock(type: "image")]),
        TranscriptEntry(seq: 2, kind: "assistant", blocks: [
            TranscriptBlock(type: "thinking", text: "hmm"), text("Running."),
            TranscriptBlock(type: "tool_use", id: "t1", name: "Bash", input: .object(["command": .string("ls")])),
        ]),
        TranscriptEntry(seq: 3, kind: "tool_result", blocks: [
            TranscriptBlock(type: "tool_result", text: "a\nb", toolUseID: "t1", isError: false),
        ]),
        TranscriptEntry(seq: 4, kind: "assistant", blocks: [
            TranscriptBlock(type: "tool_use", id: "t2", name: "Read", input: .object(["file_path": .string("/src/app/a.go")])),
        ]),
        TranscriptEntry(seq: 5, kind: "turn_end", durationMS: 61_000),
        TranscriptEntry(seq: 6, kind: "divider", reason: "compact"),
        TranscriptEntry(seq: 7, kind: "notice", blocks: [text("API error")], level: "error"),
        TranscriptEntry(seq: 8, kind: "something_new"),
    ]
    let items = ChatItem.fold(entries)
    #expect(items.map(\.id) == ["1.0", "2.0", "2.1", "2.2", "4.0", "5.0", "6.0", "7.0"])
    guard case let .tool(_, bash) = items[3], case let .tool(_, read) = items[4] else {
        Issue.record("not tools: \(items)")
        return
    }
    #expect(bash.result?.text == "a\nb")
    #expect(!bash.failed)
    #expect(read.result == nil)
    #expect(items[0] == .prompt(id: "1.0", text: "run it", images: 1))
    #expect(items[7] == .notice(id: "7.0", text: "API error", isError: true))
}

@Test func stoppedIsNotFailed() {
    var call = ToolCall(id: "t", name: "Bash", input: .object([:]), inputTruncated: false,
                        result: TranscriptBlock(type: "tool_result", text: "boom", toolUseID: "t", isError: true))
    #expect(call.failed && !call.stopped)
    call.result?.text = "[Request interrupted by user for tool use]"
    #expect(call.stopped)
}

@Test(arguments: [
    ("Bash", JSONValue.object(["command": .string("go test ./..."), "description": .string("Run tests")]), "Run", "Run tests"),
    ("Edit", .object(["file_path": .string("/src/app/internal/x.go")]), "Edit", "internal/x.go"),
    ("Read", .object(["file_path": .string("/etc/hosts")]), "Read", "/etc/hosts"),
    ("Grep", .object(["pattern": .string("func main")]), "Search", "func main"),
    ("mcp__github__create_issue", .object([:]), "github", "create_issue"),
    ("Mystery", .object([:]), "Mystery", ""),
])
func toolLabels(name: String, input: JSONValue, verb: String, subject: String) {
    let label = ToolLabel.of(ToolCall(id: "t", name: name, input: input, inputTruncated: false), cwd: "/src/app")
    #expect(label.verb == verb)
    #expect(label.subject == subject)
}

@Test func lineDiff() {
    #expect(LineDiff.diff("a\nb\nc", "a\nB\nc") == [.same("a"), .removed("b"), .added("B"), .same("c")])
    #expect(LineDiff.diff("", "x") == [.removed(""), .added("x")])
    #expect(LineDiff.diff("same", "same") == [.same("same")])
}

@Test func markdownSegments() {
    let md = """
    ## Result
    Some **bold** text.

    | File | Lines |
    |---|---:|
    | a.go | 3 |

    ```go
    func main() {}
    ```
    Done
    ```
    unclosed
    """
    #expect(MarkdownText.segments(md) == [
        .heading(2, "Result"),
        .prose("Some **bold** text."),
        .table([["File", "Lines"], ["a.go", "3"]]),
        .code("func main() {}"),
        .prose("Done"),
        .code("unclosed"),
    ])
}

@Test func durations() {
    #expect(ChatView.duration(9_400) == "9s")
    #expect(ChatView.duration(61_000) == "1m 1s")
    #expect(ChatView.duration(3_725_000) == "1h 2m")
}
