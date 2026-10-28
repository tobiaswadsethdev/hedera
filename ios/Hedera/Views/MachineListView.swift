import HederaKit
import SwiftUI

struct MachineListView: View {
    @Environment(AppModel.self) private var model
    @Binding var pendingPairURL: String?
    @State private var pairing = false
    @State private var path = NavigationPath()
    @State private var removing: MachineClient?
    @State private var error: String?

    var body: some View {
        NavigationStack(path: $path) {
            List {
                ForEach(model.machines) { m in
                    NavigationLink(value: Route.machine(m.id)) {
                        MachineRow(machine: m)
                    }
                    .swipeActions {
                        Button("Remove", role: .destructive) { removing = m }
                    }
                }
            }
            .overlay {
                if model.machines.isEmpty {
                    ContentUnavailableView {
                        Label("No machines", systemImage: "desktopcomputer")
                    } description: {
                        Text("Run `hedera pair` on a computer and scan the code it shows.")
                    } actions: {
                        Button("Pair a Machine") { pairing = true }
                            .buttonStyle(.borderedProminent)
                    }
                }
            }
            .navigationTitle("Machines")
            .toolbar {
                ToolbarItem(placement: .primaryAction) {
                    Button { pairing = true } label: { Label("Pair", systemImage: "qrcode.viewfinder") }
                }
            }
            .navigationDestination(for: Route.self) { route in
                switch route {
                case let .machine(id):
                    if let m = model.machine(id) { MachineDetailView(machine: m, path: $path) }
                case let .session(machineID, sessionID):
                    if let m = model.machine(machineID) { SessionView(machine: m, sessionID: sessionID) }
                }
            }
            .sheet(isPresented: $pairing) {
                PairView(initialURL: pendingPairURL) { client in
                    pairing = false
                    path.append(Route.machine(client.id))
                }
            }
            .onChange(of: pendingPairURL) { _, url in
                if url != nil { pairing = true }
            }
            .onChange(of: pairing) { _, open in
                if !open { pendingPairURL = nil }
            }
            .confirmationDialog("Remove \(removing?.record.name ?? "")?", isPresented: .constant(removing != nil),
                                titleVisibility: .visible, presenting: removing) { m in
                Button("Remove", role: .destructive) {
                    removing = nil
                    Task {
                        do {
                            if try await !model.remove(m) {
                                error = "\(m.record.name) was offline, so its token couldn't be revoked. Run `hedera tokens revoke \(m.record.pairingID)` on it."
                            }
                        } catch {
                            self.error = error.localizedDescription
                        }
                    }
                }
                Button("Cancel", role: .cancel) { removing = nil }
            } message: { _ in
                Text("This phone's token is revoked on the machine and deleted from the Keychain.")
            }
            .alert("Removed", isPresented: .constant(error != nil)) {
                Button("OK") { error = nil }
            } message: {
                Text(error ?? "")
            }
        }
    }
}

enum Route: Hashable {
    case machine(String)
    case session(machineID: String, sessionID: String)
}

struct MachineRow: View {
    let machine: MachineClient

    var body: some View {
        HStack(spacing: 12) {
            Image(systemName: "desktopcomputer")
                .font(.title2)
                .foregroundStyle(.tint)
                .frame(width: 34)
            VStack(alignment: .leading, spacing: 3) {
                HStack(spacing: 6) {
                    Text(machine.record.name).font(.headline)
                    ConnectionDot(state: machine.state)
                }
                Text(summary)
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }
        }
        .padding(.vertical, 4)
    }

    private var summary: String {
        guard machine.state == .online || (machine.state == .connecting && machine.hello != nil) else {
            return machine.state.label
        }
        do {
            let live = machine.sessions.filter { $0.exit == nil }
            var parts = ["\(live.count) session\(live.count == 1 ? "" : "s")"]
            let attention = live.filter(\.needsAttention).count
            if attention > 0 { parts.append("\(attention) need\(attention == 1 ? "s" : "") you") }
            let working = live.filter { $0.displayStatus == "working" }.count
            if working > 0 { parts.append("\(working) working") }
            return parts.joined(separator: " · ")
        }
    }
}
