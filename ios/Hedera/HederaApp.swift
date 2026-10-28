import HederaKit
import SwiftUI

@main
struct HederaApp: App {
    @UIApplicationDelegateAdaptor private var delegate: AppDelegate
    private var model: AppModel { delegate.model }
    @State private var lock = AppLock()
    @State private var pendingPairURL: String?
    @Environment(\.scenePhase) private var scenePhase

    var body: some Scene {
        WindowGroup {
            ZStack {
                MachineListView(pendingPairURL: $pendingPairURL)
                // Covers the content while locked, and whenever the app isn't
                // active so the app switcher snapshot shows nothing.
                if lock.isLocked || scenePhase != .active {
                    LockView(lock: lock)
                        .transition(.opacity)
                }
            }
            .animation(.default, value: lock.isLocked)
            .environment(model)
            .environment(lock)
            .onChange(of: lock.isLocked) { _, locked in
                // The terminal's keyboard lives in its own window above the
                // lock cover; take it down so nothing can be typed while locked.
                if locked { dismissKeyboard() }
            }
            .task {
                // Launch: scenePhase may already be .active, so onChange
                // wouldn't fire.
                model.resumeAll()
                await lock.unlock()
            }
            .onOpenURL { url in
                // A hedera://pair link (e.g. from the Camera app) opens the
                // pairing screen; nothing is sent until the user confirms.
                if url.scheme == "hedera", url.host == "pair" {
                    pendingPairURL = url.absoluteString
                }
            }
        }
        .onChange(of: scenePhase) { _, phase in
            switch phase {
            case .active:
                model.resumeAll()
                Task { await lock.unlock() }
            case .background:
                lock.lock()
                dismissKeyboard()
                model.suspendAll()
            default:
                break
            }
        }
    }
}

@MainActor
private func dismissKeyboard() {
    UIApplication.shared.sendAction(#selector(UIResponder.resignFirstResponder), to: nil, from: nil, for: nil)
}

struct LockView: View {
    let lock: AppLock

    var body: some View {
        ZStack {
            Color("LaunchBackground").ignoresSafeArea()
            VStack(spacing: 20) {
                Image(systemName: "leaf.fill")
                    .font(.system(size: 56))
                    .foregroundStyle(.tint)
                Text("Hedera")
                    .font(.largeTitle.weight(.semibold))
                    .foregroundStyle(.white)
                if lock.isLocked {
                    Button {
                        Task { await lock.unlock() }
                    } label: {
                        Label("Unlock", systemImage: "faceid")
                            .frame(minWidth: 160)
                    }
                    .buttonStyle(.borderedProminent)
                    .disabled(lock.isAuthenticating)
                    if let error = lock.lastError {
                        Text(error)
                            .font(.footnote)
                            .foregroundStyle(.white.opacity(0.7))
                            .multilineTextAlignment(.center)
                            .padding(.horizontal)
                    }
                }
            }
        }
    }
}
