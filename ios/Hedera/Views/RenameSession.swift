import HederaKit
import SwiftUI

extension MachineClient {
    /// The agent takes `session.rename`.
    var canRename: Bool { hello?.capabilities.contains("rename") == true }
}

/// An alert with a text field for a new session title. Set `session` to show it.
private struct RenameSessionAlert: ViewModifier {
    let machine: MachineClient
    @Binding var session: Session?
    @State private var title = ""
    @State private var error: String?

    func body(content: Content) -> some View {
        content
            .alert("Rename Session", isPresented: Binding(
                get: { session != nil },
                set: { if !$0 { session = nil } }
            )) {
                TextField("Title", text: $title)
                Button("Cancel", role: .cancel) {}
                Button("Rename") {
                    guard let id = session?.id else { return }
                    let new = title.trimmingCharacters(in: .whitespacesAndNewlines)
                    Task {
                        do { try await machine.renameSession(id, title: new) } catch { self.error = error.localizedDescription }
                    }
                }
                .disabled(title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            }
            .onChange(of: session?.id) {
                if let session { title = session.title }
            }
            .alert("Couldn't rename", isPresented: .constant(error != nil)) {
                Button("OK") { error = nil }
            } message: {
                Text(error ?? "")
            }
    }
}

extension View {
    func renameSessionAlert(machine: MachineClient, session: Binding<Session?>) -> some View {
        modifier(RenameSessionAlert(machine: machine, session: session))
    }
}
