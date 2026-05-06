import Cocoa
import UserNotifications

// ── Argument parsing ──────────────────────────────────────────────────────────

struct Args {
    var title   = "MDM Watch"
    var message = ""
    var openURL = ""
}

func parseArgs() -> Args {
    var a = Args()
    let argv = CommandLine.arguments
    var i = 1
    while i < argv.count {
        switch argv[i] {
        case "-title":   i += 1; if i < argv.count { a.title   = argv[i] }
        case "-message": i += 1; if i < argv.count { a.message = argv[i] }
        case "-open":    i += 1; if i < argv.count { a.openURL = argv[i] }
        default: break
        }
        i += 1
    }
    return a
}

// ── App delegate ──────────────────────────────────────────────────────────────

class AppDelegate: NSObject, NSApplicationDelegate, UNUserNotificationCenterDelegate {

    let center = UNUserNotificationCenter.current()

    func applicationDidFinishLaunching(_ note: Notification) {
        center.delegate = self

        // Register the "Open Details" action category
        let action = UNNotificationAction(
            identifier: "OPEN",
            title: "Open Details",
            options: [.foreground]
        )
        let category = UNNotificationCategory(
            identifier: "MDM_WATCH",
            actions: [action],
            intentIdentifiers: [],
            options: []
        )
        center.setNotificationCategories([category])

        let args = parseArgs()

        if !args.message.isEmpty {
            // ── Send mode: called by mdm-watch daemon ─────────────────────────
            center.requestAuthorization(options: [.alert, .sound]) { granted, error in
                guard granted else {
                    fputs("mdm-notifier: notification permission denied\n", stderr)
                    DispatchQueue.main.async { NSApp.terminate(nil) }
                    return
                }
                self.send(title: args.title, message: args.message, openURL: args.openURL)
            }
        } else {
            // ── Click-handler mode: macOS relaunched us after a notification tap
            // The delegate method below will fire shortly; give it 3 s then quit.
            DispatchQueue.main.asyncAfter(deadline: .now() + 3) {
                NSApp.terminate(nil)
            }
        }
    }

    private func send(title: String, message: String, openURL: String) {
        let content = UNMutableNotificationContent()
        content.title = title
        content.body  = message
        content.sound = .default
        content.categoryIdentifier = "MDM_WATCH"
        if !openURL.isEmpty {
            content.userInfo = ["open": openURL]
        }

        let req = UNNotificationRequest(
            identifier: UUID().uuidString,
            content: content,
            trigger: nil   // deliver immediately
        )
        center.add(req) { error in
            if let error = error {
                fputs("mdm-notifier: \(error)\n", stderr)
            }
            // Wait briefly so the notification is actually delivered before we exit
            DispatchQueue.main.asyncAfter(deadline: .now() + 1.5) {
                NSApp.terminate(nil)
            }
        }
    }

    // Called when the user clicks the notification (or the "Open Details" action)
    func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        didReceive response: UNNotificationResponse,
        withCompletionHandler done: @escaping () -> Void
    ) {
        let info = response.notification.request.content.userInfo
        if let urlString = info["open"] as? String, let url = URL(string: urlString) {
            NSWorkspace.shared.open(url)
        }
        done()
        NSApp.terminate(nil)
    }

    // Show notification even when app is in foreground (shouldn't happen, but just in case)
    func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        willPresent notification: UNNotification,
        withCompletionHandler done: @escaping (UNNotificationPresentationOptions) -> Void
    ) {
        done([.banner, .sound])
    }
}

// ── Main ──────────────────────────────────────────────────────────────────────

let app = NSApplication.shared
app.setActivationPolicy(.accessory)   // no Dock icon
let delegate = AppDelegate()
app.delegate = delegate
app.run()
