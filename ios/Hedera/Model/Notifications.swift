import HederaKit
import UIKit
import UserNotifications

/// The `hedera` part of a notification's payload (protocol/radix-api.md).
struct NotificationTarget: Equatable, Sendable {
    var machineID: String
    var sessionID: String
    var turn: Int?

    init?(userInfo: [AnyHashable: Any]) {
        guard let h = userInfo["hedera"] as? [String: Any],
              let machineID = h["machine_id"] as? String,
              let sessionID = h["session_id"] as? String
        else { return nil }
        self.machineID = machineID
        self.sessionID = sessionID
        turn = (h["turn"] as? NSNumber)?.intValue
    }

    var threadID: String { "\(machineID):\(sessionID)" }

    var userInfo: [String: Any] {
        var h: [String: Any] = ["machine_id": machineID, "session_id": sessionID]
        if let turn { h["turn"] = turn }
        return ["hedera": h]
    }
}

enum NotificationCategories {
    static let replyAction = "REPLY"

    /// Radix picks the category from the event kind. Claude's turn and input
    /// notifications can be answered from the lock screen; the reply requires
    /// unlocking the phone, since it types into a terminal.
    static func register() {
        let reply = UNTextInputNotificationAction(identifier: replyAction, title: "Reply",
                                                  options: [.authenticationRequired],
                                                  textInputButtonTitle: "Send", textInputPlaceholder: "Reply to Claude")
        let categories: Set<UNNotificationCategory> = [
            UNNotificationCategory(identifier: "CLAUDE_TURN", actions: [reply], intentIdentifiers: []),
            UNNotificationCategory(identifier: "CLAUDE_INPUT", actions: [reply], intentIdentifiers: []),
            UNNotificationCategory(identifier: "CLAUDE_FAILED", actions: [], intentIdentifiers: []),
            UNNotificationCategory(identifier: "CLAUDE_ENDED", actions: [], intentIdentifiers: []),
            UNNotificationCategory(identifier: "SHELL_EXITED", actions: [], intentIdentifiers: []),
            UNNotificationCategory(identifier: "TEST", actions: [], intentIdentifiers: []),
        ]
        UNUserNotificationCenter.current().setNotificationCategories(categories)
    }
}

/// Owns the model so notification callbacks, which can arrive before any view
/// exists (a reply launches the app in the background), have it at hand.
final class AppDelegate: NSObject, UIApplicationDelegate, UNUserNotificationCenterDelegate {
    let model = AppModel()

    func application(_ application: UIApplication,
                     didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]? = nil) -> Bool {
        UNUserNotificationCenter.current().delegate = self
        NotificationCategories.register()
        Task { await model.registerForPushIfAllowed() }
        return true
    }

    func application(_ application: UIApplication, didRegisterForRemoteNotificationsWithDeviceToken deviceToken: Data) {
        Task { await model.deviceTokenChanged(deviceToken) }
    }

    func application(_ application: UIApplication, didFailToRegisterForRemoteNotificationsWithError error: Error) {
        model.deviceTokenFailed(error)
    }

    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification)
        async -> UNNotificationPresentationOptions {
        let target = NotificationTarget(userInfo: notification.request.content.userInfo)
        return await MainActor.run {
            if let target, let visible = model.visibleSession,
               visible.machineID == target.machineID, visible.sessionID == target.sessionID {
                return []
            }
            return [.banner, .list, .sound]
        }
    }

    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse) async {
        guard let target = NotificationTarget(userInfo: response.notification.request.content.userInfo) else { return }
        let action = response.actionIdentifier
        let text = (response as? UNTextInputNotificationResponse)?.userText
        // iOS keeps the app running until this returns, so the reply is awaited.
        if action == NotificationCategories.replyAction, let text {
            await model.reply(text, to: target)
        } else if action == UNNotificationDefaultActionIdentifier {
            await model.open(target)
        }
    }
}
