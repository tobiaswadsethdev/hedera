import HederaKit
import SwiftUI

extension Session {
    /// The status worth showing: Claude's, when Claude Code runs in the session.
    var displayStatus: String {
        if let c = claude, exit == nil, c.status != "ended" { return c.status }
        return status
    }

    var isClaude: Bool { kind == SessionKind.claude || (claude != nil && exit == nil && claude?.status != "ended") }

    var needsAttention: Bool { ["needs_input", "failed"].contains(displayStatus) }
}

struct StatusBadge: View {
    let status: String

    var body: some View {
        Text(label)
            .font(.caption.weight(.medium))
            .padding(.horizontal, 8)
            .padding(.vertical, 3)
            .background(color.opacity(0.18), in: Capsule())
            .foregroundStyle(color)
    }

    private var label: String {
        switch status {
        case "needs_input": "needs input"
        default: status
        }
    }

    private var color: Color {
        switch status {
        case "running", "idle": .green
        case "working", "starting": .blue
        case "needs_input": .orange
        case "failed": .red
        default: .secondary
        }
    }
}

struct ConnectionDot: View {
    let state: ConnectionState

    var body: some View {
        Circle()
            .fill(color)
            .frame(width: 9, height: 9)
            .accessibilityLabel(state.label)
    }

    private var color: Color {
        switch state {
        case .online: .green
        case .connecting: .yellow
        case .waiting, .suspended: .gray
        case .revoked: .red
        }
    }
}

extension ConnectionState {
    var label: String {
        switch self {
        case .online: "Online"
        case .connecting: "Connecting…"
        case let .waiting(error): "Offline: \(error)"
        case .suspended: "Paused"
        case let .revoked(why): why
        }
    }
}
