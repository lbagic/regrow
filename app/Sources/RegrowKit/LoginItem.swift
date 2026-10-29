import Foundation

/// Whether Regrow is one of the owner's Login Items, as the menu shows it.
public enum LoginItemState: Equatable {
    case on
    case off
    /// macOS holds the registration until the owner approves it in System Settings.
    case needsApproval

    /// The figure set against the menu's right edge.
    public var figure: String {
        switch self {
        case .on: "on"
        case .off: "off"
        case .needsApproval: "needs approval"
        }
    }

    /// What choosing the row does.
    public var choice: LoginItemChoice {
        switch self {
        case .on: .unregister
        case .off: .register
        case .needsApproval: .openSettings
        }
    }
}

public enum LoginItemChoice: Equatable {
    case register
    case unregister
    case openSettings
}
