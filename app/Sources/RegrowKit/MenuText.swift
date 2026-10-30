import Foundation

/// The menu's lines about the last tick, as text.
public enum MenuText {
    /// Days to full, or why there is no such figure.
    public static func forecast(_ tick: Wire.Tick) -> String {
        if let days = tick.daysToFull {
            if days < 1 {
                return "Full in less than a day at this rate"
            }
            let figure = String(format: "%.1f", locale: Locale(identifier: "en_US_POSIX"), days)
            return "Full in about \(figure) days at this rate"
        }
        if tick.historyUnreadable == true {
            return "No forecast: the free-space history could not be read"
        }
        switch tick.noForecast {
        case "short_history": return "No forecast yet: not enough history"
        case "not_falling": return "No forecast: free space is not falling"
        default: return "No forecast"
        }
    }

    /// A tick's terminal error. A locked gate refused the prune, not
    /// the check: the tick's figures are in the menu.
    public static func tickError(code: String?, message: String?) -> String {
        let words = message ?? code ?? "unknown error"
        return code == "autotrim_locked" ? "Autotrim did not run: \(words)" : "Last check failed: \(words)"
    }

    /// The command a line quotes in backticks for the owner to run, as
    /// Terminal takes it: a leading `regrow` is the engine's path, which
    /// no shell finds by name. Nil for a line that quotes none.
    public static func command(in line: String, engine: String) -> String? {
        let parts = line.components(separatedBy: "`")
        let quotes = stride(from: 1, to: parts.count - 1, by: 2).map { parts[$0] }
        guard let quote = quotes.first(where: { $0.contains(" ") }) else { return nil }
        return quote.hasPrefix("regrow ") ? shellWord(engine) + quote.dropFirst("regrow".count) : quote
    }

    /// One word as a shell reads it back, quoted as the page's `shellWord`.
    static func shellWord(_ word: String) -> String {
        guard word.rangeOfCharacter(from: CharacterSet(charactersIn: " \t\n'\"\\$`*?[](){}<>|&;!#")) != nil else { return word }
        return "'" + word.replacingOccurrences(of: "'", with: #"'\''"#) + "'"
    }

    /// The warning for a surface that still shows an aerial: with the
    /// picks cleared it has no videos to play, and macOS may pick it
    /// again.
    public static func aerialSet(_ surface: String) -> String {
        "An aerial is still set as your \(surface), so macOS may download for it again: choose another in System Settings ▸ \(aerialPane(surface))."
    }

    /// What an `on` could not put back.
    public static func aerialsMissing(_ n: Int) -> String {
        "\(n) recorded \(n == 1 ? "pick is" : "picks are") no longer in macOS's aerial catalogue and \(n == 1 ? "was" : "were") not put back."
    }

    /// The System Settings pane that chooses what a surface shows.
    public static func aerialPane(_ surface: String) -> String {
        surface == "screen saver" ? "Screen Saver" : "Wallpaper"
    }

    /// The link that opens that pane.
    public static func aerialPaneURL(_ surface: String) -> URL {
        URL(string: surface == "screen saver"
            ? "x-apple.systempreferences:com.apple.ScreenSaver-Settings.extension"
            : "x-apple.systempreferences:com.apple.Wallpaper-Settings.extension")!
    }

    /// Full Disk Access as the engine's `hello` found it.
    public static func fullDiskAccess(_ fda: String?) -> String {
        switch fda {
        case "granted": return "granted"
        case "denied": return "not granted"
        case nil: return "not checked yet"
        default: return "unknown"
        }
    }
}
