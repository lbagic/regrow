import AppKit
import RegrowKit
import ServiceManagement
import SwiftUI

/// The app has no scene of its own: the menu bar item is the status
/// menu's, and the scan window is the shell's, opened from the menu or by
/// opening the app.
struct RegrowApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var delegate

    var body: some Scene {
        Settings { EmptyView() }
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate {
    private var signals: [DispatchSourceSignal] = []
    private var statusMenu: StatusMenu?

    func applicationDidFinishLaunching(_ notification: Notification) {
        // A signal source only sees a signal whose default action is
        // off. Children get every default back when they are started.
        for number in [SIGTERM, SIGINT] {
            signal(number, SIG_IGN)
            let source = DispatchSource.makeSignalSource(signal: number, queue: .main)
            source.setEventHandler { MainRunLoop.quit() }
            source.resume()
            signals.append(source)
        }
        NSWorkspace.shared.notificationCenter.addObserver(
            forName: NSWorkspace.didWakeNotification, object: nil, queue: .main
        ) { _ in
            MainActor.assumeIsolated { Shell.shared.woke() }
        }
        statusMenu = StatusMenu(shell: Shell.shared)
        Shell.shared.start(atLogin: Self.loginReason())
    }

    /// Why this launch is the login's, or nil when the owner opened the
    /// app. The marker is read while the launch's open-application event
    /// is the current one.
    private static func loginReason() -> String? {
        if let event = NSAppleEventManager.shared().currentAppleEvent, event.eventID == kAEOpenApplication,
            event.paramDescriptor(forKeyword: keyAEPropData)?.enumCodeValue == keyAELaunchedAsLogInItem
        {
            return "marked as a login item"
        }
        // Only a registered login item is started by the login; the owner
        // opening the app while that item runs is a reopen.
        if LoginSession.isLoginLaunch(at: Date(), sessionStart: LoginSession.consoleStart()),
            SMAppService.mainApp.status == .enabled
        {
            return "started within \(Int(LoginSession.window)) s of the login"
        }
        return nil
    }

    /// Opening the app while it runs, from Finder, Spotlight or `open`.
    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        Shell.shared.reopened()
        return false
    }

    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        Shell.shared.shutdown {
            NSApp.reply(toApplicationShouldTerminate: true)
        }
        return .terminateLater
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
        false
    }
}
