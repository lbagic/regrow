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
    /// Nil where the app is no login item candidate: not run from ~/Applications.
    public var loginItem: LoginItemState?
    public var fda: String?
    public var environmentFailure: String?
    /// The engine's path, which a copied `regrow` command starts.
    public var engine: String
    public var aerials: AerialsSwitch

    public init(
        tick: Wire.Tick?, checkedAt: String?, tickFailure: TickFailure?, engineDown: String?,
        autotrim: Bool, fda: String?, environmentFailure: String?, engine: String,
        loginItem: LoginItemState? = nil,
        aerials: AerialsSwitch = AerialsSwitch()
    ) {
        self.tick = tick
        self.checkedAt = checkedAt
        self.tickFailure = tickFailure
        self.engineDown = engineDown
        self.autotrim = autotrim
        self.loginItem = loginItem
        self.fda = fda
        self.environmentFailure = environmentFailure
        self.engine = engine
        self.aerials = aerials
    }
}

/// The aerial downloads as the shell last heard of them, and a switch
/// it has asked for.
public struct AerialsSwitch: Equatable {
    public var status: Wire.AerialsStatus?
    /// A switch waits for the owner's password, or for the engine.
    public var pending: Bool
    /// The pending switch waits behind a scan, execute or tick, or for
    /// the engine to start: macOS has not asked yet.
    public var queued: Bool
    /// Why the last switch did not finish.
    public var failure: String?
    /// What the last switch could not do, though it finished.
    public var note: String?
    /// The state read when that switch ended.
    public var failedAt: String?
    /// The switch in flight's outcome, from its own `aerials` event.
    public var result: Wire.AerialsResult?

    public init(status: Wire.AerialsStatus? = nil, pending: Bool = false, queued: Bool = false, failure: String? = nil) {
        self.status = status
        self.pending = pending
        self.queued = queued
        self.failure = failure
    }

    /// A read no switch asked for. It keeps the last switch's words while
    /// the state is the one they were read with, or could not be read.
    public mutating func read(_ status: Wire.AerialsStatus) {
        self.status = status
        if failure != nil || note != nil, status.state != failedAt, status.state == "on" || status.state == "off" {
            failure = nil
            note = nil
            failedAt = nil
        }
    }

    /// A switch sent, or held until the engine can take it.
    public mutating func start(queued: Bool) {
        pending = true
        self.queued = queued
        failure = nil
        note = nil
        failedAt = nil
        result = nil
    }

    /// The switch's own read: the state it left and its outcome.
    public mutating func switched(_ event: Wire.Aerials) {
        status = event.status
        result = event.result
    }

    /// The switch in flight ended with an error. The engine's reads
    /// around its root step say whether it changed the state.
    public mutating func failed(_ message: String) {
        let before = result?.before?.state, after = result?.after?.state
        var why: String
        if result?.ran != true {
            why = "Aerial downloads did not switch: \(message)"
        } else if let after, after == before, after == "on" || after == "off" {
            why = "Aerial downloads are still \(after): \(message)"
        } else if let after, after == "on" || after == "off" {
            why = "Aerial downloads are \(after) now, but the switch ended with an error: \(message)"
        } else {
            why = "Aerial downloads may have switched: \(message)"
        }
        if let missing = result?.missing, missing > 0 {
            why += (why.hasSuffix(".") ? " " : ". ") + MenuText.aerialsMissing(missing)
        }
        end(failure: why)
    }

    /// The switch in flight ended without an error.
    public mutating func done() {
        end(note: (result?.missing).flatMap { $0 > 0 ? MenuText.aerialsMissing($0) : nil })
    }

    /// The switch in flight ended, with what to say about it.
    public mutating func end(failure: String? = nil, note: String? = nil) {
        pending = false
        queued = false
        result = nil
        self.failure = failure
        self.note = note
        failedAt = failure == nil && note == nil ? nil : status?.state
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
    case scan
    case autotrim(on: Bool)
    case loginItem(LoginItemState)
    /// The aerial downloads' switch: its state, and whether a click
    /// turns them off or on; a line alone when off with nothing to put
    /// back.
    case aerials(state: String, switchable: Bool)
    /// The pane in System Settings that chooses what the surface shows,
    /// under the warning that an aerial is still set there.
    case openAerialPane(surface: String)
    /// A line in secondary under a switch: why autotrim did not run, or
    /// why the aerials switch did not end and a warning for an aerial
    /// still set.
    case note(String)
    /// The command the line above quotes, as Terminal takes it.
    case copy(String)
    /// Full Disk Access and its state; while it is not granted, choosing
    /// the row opens its pane in System Settings.
    case fullDiskAccess(String, opensSettings: Bool)
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
        rows += [.separator, .open, .scan, .separator, .autotrim(on: state.autotrim)]
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
        if let loginItem = state.loginItem {
            rows.append(.loginItem(loginItem))
        }
        rows += aerials(state.aerials)
        let off = state.fda.map { $0 != "granted" } ?? false
        rows += [.separator, .fullDiskAccess(MenuText.fullDiskAccess(state.fda), opensSettings: off)]
        if off {
            rows.append(.relaunch)
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

    /// The switch shows only where regrow can turn the downloads off or
    /// on: macOS 15 and earlier, with the picks readable. Why a switch
    /// did not end, and an aerial still set, show whatever the state.
    static func aerials(_ aerials: AerialsSwitch) -> [MenuRow] {
        guard let status = aerials.status else { return [] }
        var rows: [MenuRow] = []
        if status.state == "on" || status.state == "off" {
            let on = status.state == "on"
            let waiting = status.recorded?.waiting ?? false
            let state = !aerials.pending ? status.state : aerials.queued ? "waiting…" : "asking…"
            rows.append(.aerials(state: state, switchable: !aerials.pending && (on || waiting)))
        }
        for line in [aerials.failure, aerials.note].compactMap({ $0 }) {
            rows.append(.note(line))
        }
        for surface in status.setFor ?? [] {
            rows += [.note(MenuText.aerialSet(surface)), .openAerialPane(surface: surface)]
        }
        return rows
    }

    private static func copy(in line: String, _ state: MenuState) -> [MenuRow] {
        MenuText.command(in: line, engine: state.engine).map { [.copy($0)] } ?? []
    }
}
