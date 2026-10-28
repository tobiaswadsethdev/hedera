import HederaKit
import SwiftTerm
import SwiftUI

struct SessionView: View {
    let machine: MachineClient
    let sessionID: String
    @Environment(AppLock.self) private var lock
    @Environment(\.scenePhase) private var scenePhase
    @State private var controller: TerminalController?
    @State private var ended: String?
    @State private var confirmClose = false

    private var session: Session? { machine.session(sessionID) }

    var body: some View {
        ZStack(alignment: .top) {
            Color.black.ignoresSafeArea()
            if let controller {
                TerminalHost(controller: controller)
                    .ignoresSafeArea(.container, edges: .bottom)
            }
            banner
        }
        .navigationTitle(session?.title ?? "Session")
        .navigationBarTitleDisplayMode(.inline)
        .toolbarBackground(.visible, for: .navigationBar)
        .toolbarBackground(Color.black, for: .navigationBar)
        .toolbarColorScheme(.dark, for: .navigationBar)
        .toolbar {
            ToolbarItem(placement: .principal) {
                VStack(spacing: 0) {
                    Text(session?.title ?? "Session")
                        .font(.subheadline.weight(.semibold))
                        .foregroundStyle(.white)
                        .lineLimit(1)
                    if let s = session {
                        Text(s.displayStatus.replacingOccurrences(of: "_", with: " "))
                            .font(.caption2)
                            .foregroundStyle(.white.opacity(0.6))
                    }
                }
            }
            ToolbarItem(placement: .primaryAction) {
                Menu {
                    Button {
                        _ = controller?.view.becomeFirstResponder()
                    } label: { Label("Show Keyboard", systemImage: "keyboard") }
                    if session?.exit == nil {
                        Button(role: .destructive) { confirmClose = true } label: {
                            Label("Close Session", systemImage: "xmark.circle")
                        }
                    }
                } label: {
                    Image(systemName: "ellipsis.circle")
                }
            }
        }
        .confirmationDialog("Close this session?", isPresented: $confirmClose, titleVisibility: .visible) {
            Button("Close (hang up)", role: .destructive) {
                Task { try? await machine.closeSession(sessionID) }
            }
            Button("Kill", role: .destructive) {
                Task { try? await machine.closeSession(sessionID, kill: true) }
            }
        } message: {
            Text("The process on \(machine.record.name) is ended.")
        }
        .onAppear {
            guard controller == nil else { return }
            let c = TerminalController(machine: machine, sessionID: sessionID)
            c.onEnded = { ended = $0 }
            c.inputEnabled = !lock.isLocked
            controller = c
        }
        .onChange(of: lock.isLocked) { _, locked in
            controller?.inputEnabled = !locked
            if locked {
                _ = controller?.view.resignFirstResponder()
            } else {
                _ = controller?.view.becomeFirstResponder()
            }
        }
        .onChange(of: scenePhase) { _, phase in
            // Leaving the app locks it; drop the keyboard first so iOS
            // doesn't bring it back above the lock screen.
            if phase != .active, lock.isLocked || phase == .background {
                controller?.inputEnabled = false
                _ = controller?.view.resignFirstResponder()
            }
        }
        .onDisappear {
            controller?.stop()
            controller = nil
        }
    }

    @ViewBuilder
    private var banner: some View {
        if let ended {
            Label(ended == "removed" ? "Session removed" : endedText, systemImage: "stop.circle")
                .bannerStyle()
        } else if machine.state != .online {
            Label(machine.state == .connecting ? "Reconnecting…" : machine.state.label, systemImage: "wifi.exclamationmark")
                .bannerStyle()
        }
    }

    private var endedText: String {
        guard let exit = session?.exit else { return "Process exited" }
        if let code = exit.code { return "Process exited with code \(code)" }
        return "Process ended by \(exit.signal ?? "a signal")"
    }
}

private extension View {
    func bannerStyle() -> some View {
        font(.footnote.weight(.medium))
            .padding(.horizontal, 12)
            .padding(.vertical, 6)
            .background(.ultraThinMaterial, in: Capsule())
            .padding(.top, 8)
    }
}

/// Hosts the controller's SwiftTerm view and starts the attachment once the
/// view has a size.
struct TerminalHost: UIViewRepresentable {
    let controller: TerminalController

    func makeUIView(context: Context) -> TerminalView {
        let v = controller.view
        DispatchQueue.main.async {
            _ = v.becomeFirstResponder()
        }
        return v
    }

    func updateUIView(_ view: TerminalView, context: Context) {
        // If layout didn't change the size SwiftTerm started with, sizeChanged
        // never fires; attach anyway once the view is on screen.
        if view.bounds.width > 0 { controller.start() }
        else { DispatchQueue.main.async { controller.start() } }
    }
}
