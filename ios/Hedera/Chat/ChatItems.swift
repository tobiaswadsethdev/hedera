import Foundation
import HederaKit

/// A tool call joined with its result, which arrives in a later entry.
struct ToolCall: Equatable {
    var id: String
    var name: String
    var input: JSONValue
    var inputTruncated: Bool
    var result: TranscriptBlock?

    var failed: Bool { result?.isError == true }
    /// Claude Code reports a tool cut short by Esc as an error; it's not one.
    var stopped: Bool {
        guard failed, let text = result?.text else { return false }
        return text.contains("interrupted by user") || text.contains("The user doesn't want to proceed")
            || text.contains("doesn't want to take this action")
    }
}

/// What the chat shows, folded from transcript entries.
enum ChatItem: Identifiable, Equatable {
    case prompt(id: String, text: String, images: Int)
    case text(id: String, text: String)
    case thinking(id: String, text: String)
    case tool(id: String, call: ToolCall)
    case turnEnd(id: String, ms: Int64)
    case divider(id: String, reason: String)
    case notice(id: String, text: String, isError: Bool)

    var id: String {
        switch self {
        case let .prompt(id, _, _), let .text(id, _), let .thinking(id, _), let .tool(id, _),
             let .turnEnd(id, _), let .divider(id, _), let .notice(id, _, _):
            id
        }
    }

    /// Rebuilds the list from scratch; ids are `seq.block`, so SwiftUI
    /// diffs it cheaply.
    static func fold(_ entries: [TranscriptEntry]) -> [ChatItem] {
        var results: [String: TranscriptBlock] = [:]
        for e in entries where e.kind == TranscriptEntry.Kind.toolResult {
            for b in e.blocks ?? [] {
                if let id = b.toolUseID { results[id] = b }
            }
        }
        var items: [ChatItem] = []
        for e in entries {
            let blocks = e.blocks ?? []
            func id(_ i: Int) -> String { "\(e.seq).\(i)" }
            switch e.kind {
            case TranscriptEntry.Kind.prompt:
                let text = blocks.filter { $0.type == "text" }.compactMap(\.text).joined(separator: "\n\n")
                let images = blocks.filter { $0.type == "image" }.count
                items.append(.prompt(id: id(0), text: text, images: images))
            case TranscriptEntry.Kind.assistant:
                for (i, b) in blocks.enumerated() {
                    switch b.type {
                    case "text": items.append(.text(id: id(i), text: b.text ?? ""))
                    case "thinking": items.append(.thinking(id: id(i), text: b.text ?? ""))
                    case "tool_use":
                        guard let toolID = b.id else { continue }
                        items.append(.tool(id: id(i), call: ToolCall(
                            id: toolID, name: b.name ?? "tool", input: b.input ?? .object([:]),
                            inputTruncated: b.truncated == true, result: results[toolID])))
                    default: continue
                    }
                }
            case TranscriptEntry.Kind.turnEnd:
                if let ms = e.durationMS { items.append(.turnEnd(id: id(0), ms: ms)) }
            case TranscriptEntry.Kind.divider:
                items.append(.divider(id: id(0), reason: e.reason ?? ""))
            case TranscriptEntry.Kind.notice:
                items.append(.notice(id: id(0), text: blocks.compactMap(\.text).joined(separator: "\n"), isError: e.level == "error"))
            default:
                // tool_result is folded into its call; unknown kinds are skipped.
                continue
            }
        }
        return items
    }
}

/// How a tool call is labelled: an SF Symbol, a verb and what it acts on.
struct ToolLabel: Equatable {
    var icon: String
    var verb: String
    var subject: String

    static func of(_ call: ToolCall, cwd: String) -> ToolLabel {
        let input = call.input
        let path = input["file_path"]?.string ?? input["path"]?.string ?? input["notebook_path"]?.string
        let shown = path.map { relative($0, to: cwd) } ?? ""
        switch call.name {
        case "Bash": return ToolLabel(icon: "terminal", verb: "Run", subject: input["description"]?.string ?? input["command"]?.string ?? "")
        case "Read": return ToolLabel(icon: "doc.text", verb: "Read", subject: shown)
        case "Edit", "MultiEdit": return ToolLabel(icon: "pencil", verb: "Edit", subject: shown)
        case "Write": return ToolLabel(icon: "doc.badge.plus", verb: "Write", subject: shown)
        case "NotebookEdit": return ToolLabel(icon: "pencil", verb: "Edit notebook", subject: shown)
        case "Glob": return ToolLabel(icon: "magnifyingglass", verb: "Find files", subject: input["pattern"]?.string ?? "")
        case "Grep": return ToolLabel(icon: "magnifyingglass", verb: "Search", subject: input["pattern"]?.string ?? "")
        case "WebFetch": return ToolLabel(icon: "globe", verb: "Fetch", subject: input["url"]?.string ?? "")
        case "WebSearch": return ToolLabel(icon: "globe", verb: "Search the web", subject: input["query"]?.string ?? "")
        case "Task", "Agent": return ToolLabel(icon: "person.2", verb: "Agent", subject: input["description"]?.string ?? "")
        case "TodoWrite": return ToolLabel(icon: "checklist", verb: "Update todos", subject: "")
        case "ExitPlanMode": return ToolLabel(icon: "list.bullet.clipboard", verb: "Plan", subject: "")
        case "AskUserQuestion": return ToolLabel(icon: "questionmark.bubble", verb: "Ask", subject: "")
        default:
            let parts = call.name.components(separatedBy: "__")
            if parts.count >= 3, parts[0] == "mcp" {
                return ToolLabel(icon: "puzzlepiece.extension", verb: parts[1], subject: parts[2...].joined(separator: "__"))
            }
            return ToolLabel(icon: "wrench.and.screwdriver", verb: call.name, subject: shown)
        }
    }

    static func relative(_ path: String, to cwd: String) -> String {
        let base = cwd.hasSuffix("/") ? cwd : cwd + "/"
        return path.hasPrefix(base) ? String(path.dropFirst(base.count)) : path
    }
}

/// A line diff for Edit cards: longest common subsequence over lines, which
/// is plenty for the small replacements Edit makes.
enum LineDiff {
    enum Line: Equatable {
        case same(String)
        case removed(String)
        case added(String)
    }

    static func diff(_ old: String, _ new: String) -> [Line] {
        let a = old.components(separatedBy: "\n"), b = new.components(separatedBy: "\n")
        // Large edits: show them as a replacement rather than spend time on LCS.
        guard a.count * b.count <= 250_000 else { return a.map(Line.removed) + b.map(Line.added) }
        var lcs = Array(repeating: Array(repeating: 0, count: b.count + 1), count: a.count + 1)
        for i in stride(from: a.count - 1, through: 0, by: -1) {
            for j in stride(from: b.count - 1, through: 0, by: -1) {
                lcs[i][j] = a[i] == b[j] ? lcs[i + 1][j + 1] + 1 : max(lcs[i + 1][j], lcs[i][j + 1])
            }
        }
        var out: [Line] = []
        var i = 0, j = 0
        while i < a.count, j < b.count {
            if a[i] == b[j] {
                out.append(.same(a[i])); i += 1; j += 1
            } else if lcs[i + 1][j] >= lcs[i][j + 1] {
                out.append(.removed(a[i])); i += 1
            } else {
                out.append(.added(b[j])); j += 1
            }
        }
        out += a[i...].map(Line.removed) + b[j...].map(Line.added)
        return out
    }
}
