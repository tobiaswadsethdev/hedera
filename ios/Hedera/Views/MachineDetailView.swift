import HederaKit
import SwiftUI

struct MachineDetailView: View {
    let machine: MachineClient
    @Binding var path: NavigationPath
    @Environment(AppModel.self) private var model
    @State private var creating: NewSessionKind?
    @State private var resumable: [Resumable] = []
    @State private var error: String?

    private var live: [Session] { machine.sessions.filter { $0.exit == nil } }
    private var exited: [Session] { machine.sessions.filter { $0.exit != nil }.reversed() }

    var body: some View {
        List {
            if machine.state != .online {
                Section {
                    HStack {
                        ConnectionDot(state: machine.state)
                        Text(machine.state.label).font(.subheadline)
                        Spacer()
                        if case .waiting = machine.state {
                            Button("Retry") { machine.reconnectNow() }
                        }
                    }
                }
            }
            Section("Sessions") {
                if live.isEmpty {
                    Text("Nothing running").foregroundStyle(.secondary)
                }
                ForEach(live) { s in
                    sessionRow(s)
                        .swipeActions {
                            Button("Close", role: .destructive) { run { try await machine.closeSession(s.id) } }
                        }
                }
            }
            if !resumable.isEmpty {
                Section {
                    ForEach(resumable) { r in
                        Button {
                            run {
                                let s = try await machine.resume(r, size: nil)
                                resumable.removeAll { $0.id == r.id }
                                path.append(Route.session(machineID: machine.id, sessionID: s.id))
                            }
                        } label: {
                            VStack(alignment: .leading) {
                                Text(r.title)
                                Text(r.cwd).font(.caption).foregroundStyle(.secondary)
                            }
                        }
                    }
                } header: {
                    Text("Resume Claude")
                } footer: {
                    Text("Claude sessions that were running when the agent stopped.")
                }
            }
            if !exited.isEmpty {
                Section("Exited") {
                    ForEach(exited) { s in
                        sessionRow(s)
                            .swipeActions {
                                Button("Remove", role: .destructive) { run { try await machine.removeSession(s.id) } }
                            }
                    }
                }
            }
            if model.push.isAvailable {
                NotificationSettings(machine: machine)
            }
        }
        .navigationTitle(machine.record.name)
        .refreshable { await loadResumable() }
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                Menu {
                    Button { creating = NewSessionKind(id: SessionKind.shell) } label: { Label("New Shell", systemImage: "terminal") }
                    Button { creating = NewSessionKind(id: SessionKind.claude) } label: { Label("New Claude Session", systemImage: "sparkles") }
                } label: {
                    Label("New", systemImage: "plus")
                }
                .disabled(machine.state != .online)
            }
        }
        .sheet(item: $creating) { kind in
            NewSessionView(machine: machine, kind: kind.id) { s in
                creating = nil
                path.append(Route.session(machineID: machine.id, sessionID: s.id))
            }
        }
        .task(id: machine.state) {
            if machine.state == .online { await loadResumable() }
        }
        .alert("Couldn't do that", isPresented: .constant(error != nil)) {
            Button("OK") { error = nil }
        } message: {
            Text(error ?? "")
        }
    }

    private func sessionRow(_ s: Session) -> some View {
        NavigationLink(value: Route.session(machineID: machine.id, sessionID: s.id)) {
            VStack(alignment: .leading, spacing: 4) {
                HStack {
                    Image(systemName: s.isClaude ? "sparkles" : "terminal")
                        .foregroundStyle(.secondary)
                    Text(s.title).lineLimit(1)
                    Spacer()
                    StatusBadge(status: s.displayStatus)
                }
                if let preview = s.claude?.needsInput?.message ?? s.claude?.lastMessagePreview {
                    Text(preview)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(2)
                } else if let exit = s.exit {
                    Text(exit.code.map { "exit \($0)" } ?? exit.signal ?? "exited")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                } else {
                    Text(s.cwd)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                        .truncationMode(.head)
                }
            }
        }
    }

    private func loadResumable() async {
        resumable = (try? await machine.resumable()) ?? []
    }

    private func run(_ body: @escaping () async throws -> Void) {
        Task {
            do { try await body() } catch { self.error = error.localizedDescription }
        }
    }
}

struct NewSessionKind: Identifiable {
    var id: String
}

struct NewSessionView: View {
    let machine: MachineClient
    let kind: String
    let onCreated: (Session) -> Void
    @Environment(\.dismiss) private var dismiss
    @AppStorage("lastCwd") private var cwd = "~"
    @State private var command = ""
    @State private var title = ""
    @State private var working = false
    @State private var error: String?

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    TextField("Directory", text: $cwd)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .font(.body.monospaced())
                } header: {
                    Text("Directory")
                } footer: {
                    Text("On \(machine.record.name). `~` is your home directory.")
                }
                Section {
                    TextField(kind == SessionKind.claude ? "claude" : "your login shell", text: $command)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .font(.body.monospaced())
                    TextField("Title (optional)", text: $title)
                } header: {
                    Text("Command")
                } footer: {
                    Text(kind == SessionKind.claude
                         ? "Leave empty to run claude, or add arguments, e.g. claude --continue."
                         : "Leave empty for a shell, or run a command such as htop.")
                }
                if let error {
                    Section { Text(error).foregroundStyle(.red) }
                }
            }
            .navigationTitle(kind == SessionKind.claude ? "New Claude Session" : "New Shell")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Start") { Task { await create() } }.disabled(working)
                }
            }
        }
    }

    private func create() async {
        working = true
        defer { working = false }
        let argv = ShellWords.split(command)
        let dir = cwd.trimmingCharacters(in: .whitespaces)
        do {
            let s = try await machine.createSession(SessionCreate(
                kind: kind,
                cwd: dir.isEmpty ? nil : dir,
                argv: argv.isEmpty ? nil : argv,
                title: title.isEmpty ? nil : title))
            onCreated(s)
        } catch {
            self.error = error.localizedDescription
        }
    }
}

/// Splits a command line into words, honouring single and double quotes and
/// backslash escapes, as a shell would (without expansion).
enum ShellWords {
    static func split(_ line: String) -> [String] {
        var words: [String] = []
        var current = ""
        var inWord = false
        var quote: Character?
        var escaped = false
        for ch in line {
            if escaped {
                current.append(ch)
                escaped = false
                inWord = true
            } else if ch == "\\" && quote != "'" {
                escaped = true
            } else if let q = quote {
                if ch == q { quote = nil } else { current.append(ch) }
            } else if ch == "'" || ch == "\"" {
                quote = ch
                inWord = true
            } else if ch.isWhitespace {
                if inWord { words.append(current) }
                current = ""
                inWord = false
            } else {
                current.append(ch)
                inWord = true
            }
        }
        if inWord { words.append(current) }
        return words
    }
}
