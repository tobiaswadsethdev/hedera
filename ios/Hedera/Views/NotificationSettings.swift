import HederaKit
import SwiftUI

/// The Notifications section of a machine's screen: on or off, which kinds,
/// and a test push.
struct NotificationSettings: View {
    let machine: MachineClient
    @Environment(AppModel.self) private var model
    @State private var prefs: [String: Bool] = [:]
    @State private var working = false
    @State private var message: String?

    private static let kinds: [(key: String, label: String)] = [
        ("claude_needs_input", "Claude needs input"),
        ("claude_turn_finished", "Claude finished a turn"),
        ("claude_failed", "Claude failed"),
        ("claude_ended", "Claude session ended"),
        ("shell_exited", "Shell exited on its own"),
    ]

    private var isOn: Bool { machine.record.pushLinkID != nil }
    private var canChange: Bool { machine.state == .online && !working }

    var body: some View {
        Section {
            Toggle(isOn: Binding(get: { isOn }, set: { on in change { try await model.setNotifications(on, for: machine) } })) {
                HStack {
                    Text("Notifications")
                    if working { ProgressView() }
                }
            }
            .disabled(!canChange)
            if isOn {
                ForEach(Self.kinds, id: \.key) { kind in
                    Toggle(kind.label, isOn: Binding(get: { prefs[kind.key] ?? true }, set: { value in
                        var next = prefs
                        next[kind.key] = value
                        change {
                            try await model.setNotifications(true, for: machine, prefs: next)
                            prefs = next
                        }
                    }))
                    .disabled(!canChange)
                }
                Button("Send Test Notification") {
                    change {
                        let r = try await model.push.sendTest()
                        message = r.sent ? nil : "Apple refused the test notification (\(r.apnsReason ?? "status \(r.apnsStatus)"))."
                    }
                }
                .disabled(working || model.push.device == nil)
            }
        } header: {
            Text("Notifications")
        } footer: {
            if let message = message ?? model.pushError {
                Text(message).foregroundStyle(.red)
            } else {
                Text("When Claude needs you or a session ends, \(machine.record.name) notifies this phone through Hedera's push relay. Notifications show the session title and a short message.")
            }
        }
        .task(id: machine.state) {
            guard machine.state == .online, isOn, let status = try? await machine.pushStatus() else { return }
            prefs = status.prefs ?? [:]
        }
    }

    private func change(_ body: @escaping () async throws -> Void) {
        working = true
        message = nil
        Task {
            defer { working = false }
            do { try await body() } catch { message = error.localizedDescription }
        }
    }
}
