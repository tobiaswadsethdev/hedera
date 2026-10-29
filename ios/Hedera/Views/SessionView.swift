import HederaKit
import SwiftTerm
import SwiftUI

struct SessionView: View {
    let machine: MachineClient
    let sessionID: String
    @Environment(AppLock.self) private var lock
    @Environment(AppModel.self) private var model
    @Environment(\.scenePhase) private var scenePhase
    @State private var controller: TerminalController?
    @State private var ended: String?
    @State private var confirmClose = false
    @State private var renaming: Session?
    @State private var keyboardShown = false
    @State private var feed: TranscriptFeed?
    /// Chat or terminal for Claude sessions; remembered across sessions.
    @AppStorage("claudeView") private var claudeView = "chat"

    private var session: Session? { machine.session(sessionID) }
    /// The agent can show this session as a chat.
    private var canChat: Bool {
        session?.claude?.transcript == true && machine.hello?.capabilities.contains("transcript") == true
    }
    private var showingChat: Bool { canChat && claudeView == "chat" }

    var body: some View {
        ZStack(alignment: .top) {
            Color.black.ignoresSafeArea()
            if showingChat, let feed {
                ChatView(machine: machine, sessionID: sessionID, feed: feed) { switchView(to: "terminal") }
            } else if let controller {
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
            if canChat {
                ToolbarItem(placement: .primaryAction) {
                    Button {
                        switchView(to: showingChat ? "terminal" : "chat")
                    } label: {
                        Image(systemName: showingChat ? "terminal" : "text.bubble")
                    }
                    .accessibilityLabel(showingChat ? "Show Terminal" : "Show Chat")
                }
            }
            if !showingChat {
                ToolbarItem(placement: .primaryAction) {
                    Button {
                        if keyboardShown {
                            _ = controller?.view.resignFirstResponder()
                        } else {
                            _ = controller?.view.becomeFirstResponder()
                        }
                    } label: {
                        Image(systemName: keyboardShown ? "keyboard.chevron.compact.down" : "keyboard")
                    }
                    .accessibilityLabel(keyboardShown ? "Hide Keyboard" : "Show Keyboard")
                }
            }
            ToolbarItem(placement: .primaryAction) {
                Menu {
                    if machine.canRename {
                        Button { renaming = session } label: { Label("Rename", systemImage: "pencil") }
                            .disabled(machine.state != .online)
                    }
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
        .renameSessionAlert(machine: machine, session: $renaming)
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
        .task(id: session?.isClaude) {
            if session?.isClaude == true, feed == nil {
                feed = machine.openTranscript(sessionID: sessionID)
            }
        }
        .onAppear {
            model.visibleSession = (machine.id, sessionID)
            guard controller == nil else { return }
            let c = TerminalController(machine: machine, sessionID: sessionID)
            c.onEnded = { ended = $0 }
            c.onFocusChange = { keyboardShown = $0 }
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
            if model.visibleSession?.sessionID == sessionID { model.visibleSession = nil }
            feed?.close()
            feed = nil
            controller?.stop()
            controller = nil
        }
    }

    private func switchView(to view: String) {
        if view == "chat" { _ = controller?.view.resignFirstResponder() }
        withAnimation(.snappy) { claudeView = view }
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
