import Darwin
import Foundation

/// When the owner's console session began. macOS documents no launch
/// reason for an `SMAppService` login item, so a launch this soon after
/// the login counts as the login's.
public enum LoginSession {
    /// The span after the login in which a launch is the login's. A launch
    /// stamped up to `early` before the login record counts too: the
    /// record and a login item's start race.
    public static let window: TimeInterval = 120
    public static let early: TimeInterval = 30

    /// The latest console login of `user` in utmpx, or nil without one.
    public static func consoleStart(user: String = NSUserName()) -> Date? {
        setutxent()
        defer { endutxent() }
        var latest: Date?
        while let entry = getutxent() {
            let record = entry.pointee
            guard Int32(record.ut_type) == USER_PROCESS,
                text(record.ut_line) == "console", text(record.ut_user) == user
            else { continue }
            let at = Date(timeIntervalSince1970: TimeInterval(record.ut_tv.tv_sec))
            if latest.map({ at > $0 }) ?? true {
                latest = at
            }
        }
        return latest
    }

    public static func isLoginLaunch(at launch: Date, sessionStart: Date?) -> Bool {
        guard let sessionStart else { return false }
        let since = launch.timeIntervalSince(sessionStart)
        return since >= -early && since < window
    }

    private static func text<Field>(_ field: Field) -> String {
        withUnsafeBytes(of: field) { String(decoding: $0.prefix { $0 != 0 }, as: UTF8.self) }
    }
}
