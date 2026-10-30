import Foundation

/// What the window's General section shows of the app and its grants,
/// as the shell last read them from macOS. Full Disk Access is the
/// engine's to say, in its `hello`; the version and the engine's path
/// are there too.
public struct WindowSettings: Equatable {
    /// Whether macOS lets the app do something only it can allow.
    public enum Grant: String {
        case allowed
        case denied
        case notAsked = "not_asked"
        case unknown
    }

    /// Nil where the app is no login item candidate: not run from ~/Applications.
    public var loginItem: LoginItemState?
    public var notifications: Grant
    public var finder: Grant
    public var log: String
    /// Nil before the engine has said where its state directory is.
    public var staging: String?
    /// The autotrim switch, as the menu's.
    public var autotrim: Bool

    public init(loginItem: LoginItemState?, notifications: Grant, finder: Grant, log: String, staging: String?, autotrim: Bool = false) {
        self.loginItem = loginItem
        self.notifications = notifications
        self.finder = finder
        self.log = log
        self.staging = staging
        self.autotrim = autotrim
    }

    /// The shell event that hands the page these settings.
    public var line: String {
        var fields: [String: Any] = ["notifications": notifications.rawValue, "finder": finder.rawValue, "log": log, "autotrim": autotrim]
        if let loginItem {
            fields["login_item"] = loginItem.wire
        }
        if let staging {
            fields["staging"] = staging
        }
        return Router.shellEvent("settings", fields)
    }
}

extension LoginItemState {
    /// The state as the page reads it in a `settings` event.
    var wire: String {
        switch self {
        case .on: "on"
        case .off: "off"
        case .needsApproval: "needs_approval"
        }
    }
}
