import HederaKit
import SwiftUI

/// One tool call: a header that says what it did, and a body to expand.
struct ToolCard: View {
    let call: ToolCall
    let cwd: String
    /// Claude is still working, so a call without a result is running.
    let working: Bool
    @State private var expanded: Bool?
    @State private var showAll = false

    private var label: ToolLabel { .of(call, cwd: cwd) }
    private var isOpen: Bool { expanded ?? opensItself }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Button {
                withAnimation(.snappy) { expanded = !isOpen }
            } label: {
                HStack(spacing: 8) {
                    Image(systemName: label.icon)
                        .frame(width: 18)
                        .foregroundStyle(.secondary)
                    Text(label.verb).fontWeight(.semibold)
                    Text(label.subject)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                        .truncationMode(.middle)
                    Spacer(minLength: 4)
                    status
                    Image(systemName: "chevron.right")
                        .font(.caption2.weight(.semibold))
                        .foregroundStyle(.tertiary)
                        .rotationEffect(.degrees(isOpen ? 90 : 0))
                }
                .font(.subheadline)
                .contentShape(Rectangle())
                .padding(.horizontal, 12)
                .padding(.vertical, 9)
            }
            .buttonStyle(.plain)
            if isOpen {
                Divider().opacity(0.4)
                detail
                    .padding(12)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        .background(Color.white.opacity(0.05), in: RoundedRectangle(cornerRadius: 10))
        .overlay(RoundedRectangle(cornerRadius: 10).strokeBorder(borderColor, lineWidth: 1))
    }

    private var borderColor: Color {
        call.failed && !call.stopped ? .red.opacity(0.5) : .white.opacity(0.08)
    }

    @ViewBuilder
    private var status: some View {
        if call.result == nil {
            if working { ProgressView().controlSize(.mini) }
        } else if call.stopped {
            Text("stopped").font(.caption).foregroundStyle(.secondary)
        } else if call.failed {
            Image(systemName: "xmark.circle.fill").foregroundStyle(.red)
        }
    }

    /// Errors, todos and small edits are worth seeing without a tap.
    private var opensItself: Bool {
        if call.failed && !call.stopped { return true }
        switch call.name {
        case "TodoWrite": return true
        case "Edit": return diff.count <= 12
        default: return false
        }
    }

    private var diff: [LineDiff.Line] {
        LineDiff.diff(call.input["old_string"]?.string ?? "", call.input["new_string"]?.string ?? "")
    }

    @ViewBuilder
    private var detail: some View {
        VStack(alignment: .leading, spacing: 8) {
            switch call.name {
            case "Bash":
                CodeBlock(text: "$ " + (call.input["command"]?.string ?? ""))
                output
            case "Edit":
                DiffView(lines: diff)
                errorOutput
            case "MultiEdit":
                if case let .array(edits)? = call.input["edits"] {
                    ForEach(Array(edits.enumerated()), id: \.offset) { _, e in
                        DiffView(lines: LineDiff.diff(e["old_string"]?.string ?? "", e["new_string"]?.string ?? ""))
                    }
                }
                errorOutput
            case "Write":
                CodeBlock(text: lastLines(call.input["content"]?.string ?? "", 40, fromStart: true))
                errorOutput
            case "TodoWrite":
                TodoList(todos: call.input["todos"])
            default:
                if call.input != .object([:]) {
                    CodeBlock(text: Self.pretty(call.input))
                }
                output
            }
            if call.inputTruncated {
                Text("Input cut to 16 KiB per field.").font(.caption2).foregroundStyle(.secondary)
            }
        }
    }

    @ViewBuilder
    private var output: some View {
        if let r = call.result {
            let text = r.text ?? ""
            let lines = text.components(separatedBy: "\n")
            if !text.isEmpty {
                CodeBlock(text: showAll ? text : lastLines(text, 30))
                    .foregroundStyle(call.failed && !call.stopped ? .red : .primary)
                if lines.count > 30 && !showAll {
                    Button("Show \(lines.count - 30) earlier lines") { showAll = true }
                        .font(.caption)
                }
            }
            if let n = r.images {
                Label("\(n) image\(n == 1 ? "" : "s")", systemImage: "photo").font(.caption).foregroundStyle(.secondary)
            }
            if r.truncated == true {
                Text("Output cut to 16 KiB.").font(.caption2).foregroundStyle(.secondary)
            }
        }
    }

    @ViewBuilder
    private var errorOutput: some View {
        if call.failed, let text = call.result?.text {
            Text(text).font(.caption).foregroundStyle(call.stopped ? .secondary : Color.red)
        }
    }

    private func lastLines(_ text: String, _ n: Int, fromStart: Bool = false) -> String {
        let lines = text.components(separatedBy: "\n")
        guard lines.count > n else { return text }
        return fromStart
            ? lines.prefix(n).joined(separator: "\n") + "\n… \(lines.count - n) more lines"
            : lines.suffix(n).joined(separator: "\n")
    }

    static func pretty(_ v: JSONValue) -> String {
        let enc = JSONEncoder()
        enc.outputFormatting = [.prettyPrinted, .sortedKeys, .withoutEscapingSlashes]
        return (try? enc.encode(v)).map { String(decoding: $0, as: UTF8.self) } ?? ""
    }
}

struct DiffView: View {
    let lines: [LineDiff.Line]

    var body: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            VStack(alignment: .leading, spacing: 0) {
                ForEach(Array(lines.enumerated()), id: \.offset) { _, line in
                    row(line)
                }
            }
            .padding(.vertical, 6)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color.white.opacity(0.04), in: RoundedRectangle(cornerRadius: 8))
        .textSelection(.enabled)
    }

    private func row(_ line: LineDiff.Line) -> some View {
        let (mark, text, color): (String, String, Color) = switch line {
        case let .same(t): (" ", t, .clear)
        case let .removed(t): ("−", t, .red)
        case let .added(t): ("+", t, .green)
        }
        return Text("\(mark) \(text)")
            .font(.system(.caption, design: .monospaced))
            .padding(.horizontal, 10)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(color.opacity(0.15))
    }
}

struct TodoList: View {
    let todos: JSONValue?

    var body: some View {
        if case let .array(items)? = todos {
            VStack(alignment: .leading, spacing: 4) {
                ForEach(Array(items.enumerated()), id: \.offset) { _, t in
                    let status = t["status"]?.string
                    Label {
                        Text(t["content"]?.string ?? "")
                            .strikethrough(status == "completed")
                            .foregroundStyle(status == "completed" ? .secondary : .primary)
                    } icon: {
                        Image(systemName: status == "completed" ? "checkmark.circle.fill"
                              : status == "in_progress" ? "circle.dotted.circle" : "circle")
                            .foregroundStyle(status == "completed" ? .green : .secondary)
                    }
                    .font(.subheadline)
                }
            }
        }
    }
}
