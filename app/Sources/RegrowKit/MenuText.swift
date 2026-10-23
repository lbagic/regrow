import Foundation

/// The menu's lines about the last tick, as text.
public enum MenuText {
    public static func free(_ tick: Wire.Tick) -> String {
        "\(humanBytes(tick.free)) free of \(humanBytes(tick.total))"
    }

    public static func purgeable(_ tick: Wire.Tick) -> String {
        "Purgeable: \(humanBytes(tick.purgeable))"
    }

    public static func swap(_ tick: Wire.Tick) -> String {
        "Swap: \(humanBytes(tick.swapUsed))"
    }

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

    public static func fullDiskAccess(_ fda: String?) -> String {
        switch fda {
        case "granted": return "Full Disk Access: granted"
        case "denied": return "Full Disk Access: not granted"
        case nil: return "Full Disk Access: not checked yet"
        default: return "Full Disk Access: unknown"
        }
    }
}
