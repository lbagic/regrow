import Foundation

/// A tick's terminal error, as the engine sent it.
public struct TickFailure: Equatable {
    public let code: String?
    public let message: String?

    public init(code: String?, message: String?) {
        self.code = code
        self.message = message
    }

    var locked: Bool { code == "autotrim_locked" }
}

/// What the menu is drawn from.
public struct MenuState {
    public var tick: Wire.Tick?
    /// When the last tick came back, as the menu writes the time.
    public var checkedAt: String?
    public var tickFailure: TickFailure?
    public var engineDown: String?
    public var autotrim: Bool
    public var fda: String?
    public var environmentFailure: String?
    /// The engine's path, which a copied `regrow` command starts.
    public var engine: String

    public init(
        tick: Wire.Tick?, checkedAt: String?, tickFailure: TickFailure?, engineDown: String?,
        autotrim: Bool, fda: String?, environmentFailure: String?, engine: String
    ) {
        self.tick = tick
        self.checkedAt = checkedAt
        self.tickFailure = tickFailure
        self.engineDown = engineDown
        self.autotrim = autotrim
        self.fda = fda
        self.environmentFailure = environmentFailure
        self.engine = engine
    }
}

/// The figures at the top of the menu, which the owner reads and does
/// not click.
public struct MenuFigures: Equatable {
    /// Free space, and the total it is of; nil before the first tick.
    public var free: String?
    public var total: String?
    /// The disk's share taken and purgeable, for the meter.
    public var taken: Double
    public var purgeable: Double
    public var details: String?
    public var forecast: String?
    public var checked: String
    /// A check that failed, the engine down: one sentence each.
    public var problems: [String]

    public static let meterGap = 2.0

    /// The meter's segments across `width`, gaps taken out first. Free
    /// space is drawn to scale; purgeable shows at least 2pt however
    /// little there is, taken out of what is taken.
    public func meter(width: Double) -> (taken: Double, purgeable: Double, free: Double) {
        let room = max(0, width - 2 * Self.meterGap)
        let free = room * max(0, 1 - taken - purgeable)
        let shown = min(room - free, max(2, room * purgeable))
        return (room - free - shown, shown, free)
    }
}

/// One line of the menu, top to bottom: the figures, then the actions.
public enum MenuRow: Equatable {
    case figures(MenuFigures)
    case separator
    case open
    case autotrim(on: Bool)
    /// The engine's words under the switch: why autotrim did not run.
    case note(String)
    /// The command the line above quotes, as Terminal takes it.
    case copy(String)
    case fullDiskAccess(String)
    case openFullDiskAccess
    case relaunch
    case staging(Int64)
    case environment(String)
    case quit
}

public enum MenuModel {
    public static func rows(_ state: MenuState) -> [MenuRow] {
        var rows: [MenuRow] = [.figures(figures(state))]
        let failed = state.tickFailure.map { MenuText.tickError(code: $0.code, message: $0.message) }
        let locked = state.tickFailure?.locked ?? false
        if let failed, !locked {
            rows += copy(in: failed, state)
        }
        rows += [.separator, .open, .separator, .autotrim(on: state.autotrim)]
        var notes: [String] = []
        if let failed, locked {
            notes.append(failed)
        }
        if state.autotrim, let skipped = state.tick?.pruned?.skipped, !skipped.isEmpty {
            notes.append(skipped)
        }
        for note in notes {
            rows += [.note(note)] + copy(in: note, state)
        }
        rows += [.separator, .fullDiskAccess(MenuText.fullDiskAccess(state.fda))]
        if let fda = state.fda, fda != "granted" {
            rows += [.openFullDiskAccess, .relaunch]
        }
        if let staged = state.tick?.stagedBytes, staged > 0 {
            rows.append(.staging(staged))
        }
        if let reason = state.environmentFailure {
            rows += [.separator, .environment("Login environment not captured: \(reason)")]
        }
        return rows + [.separator, .quit]
    }

    static func figures(_ state: MenuState) -> MenuFigures {
        var problems: [String] = []
        if let failure = state.tickFailure, !failure.locked {
            problems.append(MenuText.tickError(code: failure.code, message: failure.message))
        }
        if let down = state.engineDown {
            problems.append("The engine is not running: \(down)")
        }
        let checked = state.checkedAt.map { "Checked at \($0)" } ?? "Not checked yet"
        guard let tick = state.tick, tick.total > 0 else {
            return MenuFigures(
                free: nil, total: nil, taken: 0, purgeable: 0, details: nil, forecast: nil,
                checked: checked, problems: problems)
        }
        let total = Double(tick.total)
        return MenuFigures(
            free: humanBytes(tick.free), total: "free of \(humanBytes(tick.total))",
            taken: Double(max(0, tick.total - tick.free - tick.purgeable)) / total,
            purgeable: Double(tick.purgeable) / total,
            details: "Purgeable \(humanBytes(tick.purgeable)) · Swap \(humanBytes(tick.swapUsed))",
            forecast: MenuText.forecast(tick), checked: checked, problems: problems)
    }

    private static func copy(in line: String, _ state: MenuState) -> [MenuRow] {
        MenuText.command(in: line, engine: state.engine).map { [.copy($0)] } ?? []
    }
}
