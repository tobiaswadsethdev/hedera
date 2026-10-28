import Foundation
import LocalAuthentication
import Observation

/// Locks the app whenever it opens or returns from the background, and
/// unlocks with Face ID (or the passcode as a fallback).
@MainActor
@Observable
final class AppLock {
    private(set) var isLocked = true
    private(set) var isAuthenticating = false
    private(set) var lastError: String?

    func lock() {
        isLocked = true
    }

    func unlock() async {
        guard isLocked, !isAuthenticating else { return }
        isAuthenticating = true
        defer { isAuthenticating = false }
        let context = LAContext()
        context.localizedFallbackTitle = "Use Passcode"
        var error: NSError?
        guard context.canEvaluatePolicy(.deviceOwnerAuthentication, error: &error) else {
            if (error as? LAError)?.code == .passcodeNotSet {
                // Without a passcode the device itself is unprotected; a lock
                // here would only be theatre. (The simulator lands here too.)
                isLocked = false
            } else {
                lastError = error?.localizedDescription
            }
            return
        }
        do {
            try await context.evaluatePolicy(.deviceOwnerAuthentication, localizedReason: "Unlock Hedera")
            isLocked = false
            lastError = nil
        } catch let e as LAError where e.code == .userCancel || e.code == .appCancel || e.code == .systemCancel {
            lastError = nil
        } catch {
            lastError = error.localizedDescription
        }
    }
}
