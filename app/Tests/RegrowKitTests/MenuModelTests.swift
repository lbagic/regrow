import Foundation
import RegrowKit
import Testing

@Suite("Menu rows")
struct MenuModelTests {
    private static let engine = "/Users/dev/Applications/Regrow.app/Contents/MacOS/regrow-engine"
    private static let lock = "autotrim is locked for go-build-cache until `regrow prune go-build --yes` has completed once"

    private func tick(_ extra: String = "") throws -> Wire.Tick {
        let line = #"{"event":"tick","total":494384795648,"free":66201620480,"purgeable":4042354442,"swap_used":2108030976,"no_forecast":"short_history"\#(extra)}"#
        return try #require(Wire.decode(Wire.Tick.self, from: line))
    }

    private func state(
        tick: Wire.Tick?, failure: TickFailure? = nil, down: String? = nil, autotrim: Bool = false,
        fda: String? = "granted", environment: String? = nil
    ) -> MenuState {
        MenuState(
            tick: tick, checkedAt: "11:21", tickFailure: failure, engineDown: down, autotrim: autotrim, fda: fda,
            environmentFailure: environment, engine: Self.engine)
    }

    private func figuresOf(_ rows: [MenuRow]) -> MenuFigures? {
        if case .figures(let figures) = rows.first { return figures }
        return nil
    }

    @Test("this Mac on a quiet tick: the figures, then open, the switch, Full Disk Access and quit")
    func quiet() throws {
        let rows = MenuModel.rows(state(tick: try tick()))
        let figures = try #require(figuresOf(rows))
        #expect(figures.free == "61.7 GiB")
        #expect(figures.total == "free of 460.4 GiB")
        #expect(figures.details == "Purgeable 3.8 GiB · Swap 2.0 GiB")
        #expect(figures.forecast == "No forecast yet: not enough history")
        #expect(figures.checked == "Checked at 11:21")
        #expect(figures.problems.isEmpty)
        #expect(abs(figures.taken - 0.858) < 0.001)
        #expect(abs(figures.purgeable - 0.008) < 0.001)
        #expect(Array(rows.dropFirst()) == [
            .separator, .open, .scan, .separator, .autotrim(on: false), .separator, .fullDiskAccess("granted", opensSettings: false), .separator, .quit,
        ])
    }

    @Test("Start with Mac sits under the autotrim switch with its state, and is absent where the app is no login item")
    func loginItem() throws {
        for (item, figure, choice) in [
            (LoginItemState.on, "on", LoginItemChoice.unregister),
            (.off, "off", .register),
            (.needsApproval, "needs approval", .openSettings),
        ] {
            var s = state(tick: try tick())
            s.loginItem = item
            let rows = MenuModel.rows(s)
            let at = try #require(rows.firstIndex(of: .autotrim(on: false)))
            #expect(rows[at + 1] == .loginItem(item))
            #expect(item.figure == figure)
            #expect(item.choice == choice)
        }
        let none = MenuModel.rows(state(tick: try tick()))
        #expect(!none.contains { if case .loginItem = $0 { true } else { false } })
    }

    @Test("the meter draws free space to scale, a nearly full disk included", arguments: [
        Int64(66_201_620_480), 10 << 30, 5 << 30, 2 << 30, 0,
    ])
    func meter(free: Int64) throws {
        let line = #"{"event":"tick","total":494384795648,"free":\#(free),"purgeable":4042354442,"swap_used":0}"#
        let rows = MenuModel.rows(state(tick: try #require(Wire.decode(Wire.Tick.self, from: line))))
        let width = try #require(figuresOf(rows)).meter(width: 312)
        let room = 312 - 2 * MenuFigures.meterGap
        #expect(abs(width.free - room * Double(free) / 494_384_795_648) < 0.01)
        #expect(width.purgeable >= 2)
        #expect(width.taken >= 0)
        #expect(abs(width.taken + width.purgeable + width.free - room) < 0.01)
    }

    @Test("Full Disk Access not granted is a row that opens its settings, with Relaunch to apply under it", arguments: [
        ("denied", "not granted"), ("unknown", "unknown"),
    ])
    func fullDiskAccessOff(fda: String, figure: String) throws {
        let rows = MenuModel.rows(state(tick: try tick(), fda: fda))
        let at = try #require(rows.firstIndex(of: .fullDiskAccess(figure, opensSettings: true)))
        #expect(rows[at + 1] == .relaunch)
    }

    @Test("before the first tick the figures say so, and every action is there")
    func noTick() throws {
        let rows = MenuModel.rows(MenuState(
            tick: nil, checkedAt: nil, tickFailure: nil, engineDown: nil, autotrim: false, fda: nil,
            environmentFailure: nil, engine: Self.engine))
        let figures = try #require(figuresOf(rows))
        #expect(figures.free == nil)
        #expect(figures.checked == "Not checked yet")
        #expect(rows.contains(.open))
        #expect(rows.contains(.fullDiskAccess("not checked yet", opensSettings: false)))
        #expect(!rows.contains(.relaunch))
        #expect(rows.last == .quit)
    }

    @Test("a locked gate is a note under the switch, its command copied right under it")
    func locked() throws {
        let rows = MenuModel.rows(state(
            tick: try tick(), failure: TickFailure(code: "autotrim_locked", message: Self.lock), autotrim: true))
        #expect(figuresOf(rows)?.problems == [])
        let at = try #require(rows.firstIndex(of: .autotrim(on: true)))
        #expect(rows[at + 1] == .note("Autotrim did not run: " + Self.lock))
        #expect(rows[at + 2] == .copy(Self.engine + " prune go-build --yes"))
    }

    @Test("a skipped prune's reason is a note under the switch while autotrim is on, and not when it is off")
    func skipped() throws {
        let pruned = #","pruned":{"rule_id":"go-build-cache","skipped":"\#(Self.lock)","files":0,"bytes":0,"free_before":0,"free_after":0}"#
        let on = MenuModel.rows(state(tick: try tick(pruned), autotrim: true))
        #expect(on.contains(.note(Self.lock)))
        #expect(on.contains(.copy(Self.engine + " prune go-build --yes")))
        let off = MenuModel.rows(state(tick: try tick(pruned), autotrim: false))
        #expect(!off.contains { if case .note = $0 { return true } else { return false } })
    }

    @Test("a failed check and a stopped engine are in the figures; a command one quotes is copied under them")
    func problems() throws {
        let rows = MenuModel.rows(state(
            tick: try tick(), failure: TickFailure(code: "tick_failed", message: "`go env GOCACHE` failed"),
            down: "it exited with status 2"))
        #expect(figuresOf(rows)?.problems == [
            "Last check failed: `go env GOCACHE` failed", "The engine is not running: it exited with status 2",
        ])
        #expect(rows[1] == .copy("go env GOCACHE"))
        #expect(rows[2] == .separator)
    }

    @Test("every line showing keeps every action the menu has")
    func everything() throws {
        let rows = MenuModel.rows(state(
            tick: try tick(#","staged_bytes":2254857830"#), failure: TickFailure(code: "autotrim_locked", message: Self.lock),
            autotrim: true, fda: "denied", environment: "the login shell gave no answer within 5 s"))
        for action: MenuRow in [
            .open, .scan, .autotrim(on: true), .copy(Self.engine + " prune go-build --yes"), .fullDiskAccess("not granted", opensSettings: true), .relaunch,
            .staging(2_254_857_830), .quit,
        ] {
            #expect(rows.contains(action), "\(action)")
        }
        #expect(rows.contains(.environment("Login environment not captured: the login shell gave no answer within 5 s")))
    }

    private func aerials(_ line: String, pending: Bool = false, queued: Bool = false, failure: String? = nil) throws -> MenuState {
        var state = state(tick: try tick())
        let event = try #require(Wire.decode(Wire.Aerials.self, from: line))
        state.aerials = AerialsSwitch(status: event.status, pending: pending, queued: queued, failure: failure)
        return state
    }

    @Test("the aerial downloads' switch sits under autotrim while regrow can turn them off or on")
    func aerialsSwitch() throws {
        let on = MenuModel.rows(try aerials(#"{"event":"aerials","status":{"state":"on","counts":{"categories":4}}}"#))
        #expect(on.contains(.aerials(state: "on", switchable: true)))
        let index = try #require(on.firstIndex(of: .aerials(state: "on", switchable: true)))
        #expect(on[index - 1] == .autotrim(on: false))

        let waiting = #"{"event":"aerials","status":{"state":"off","recorded":{"cleared":"2026-10-29T12:00:00Z","picks":[{"table":"Z_2ACTIVEFORUSERS"}]}}}"#
        #expect(MenuModel.rows(try aerials(waiting)).contains(.aerials(state: "off", switchable: true)))
        let restored = #"{"event":"aerials","status":{"state":"off","recorded":{"picks":[{}],"restored":"2026-10-29T13:00:00Z"}}}"#
        #expect(MenuModel.rows(try aerials(restored)).contains(.aerials(state: "off", switchable: false)))
        #expect(MenuModel.rows(try aerials(#"{"event":"aerials","status":{"state":"off"}}"#)).contains(.aerials(state: "off", switchable: false)))

        for state in ["absent", "unknown", "unmeasured"] {
            let rows = MenuModel.rows(try aerials(#"{"event":"aerials","status":{"state":"\#(state)"}}"#))
            #expect(!rows.contains { if case .aerials = $0 { return true } else { return false } })
        }
    }

    @Test("a switch waiting for the password, a failed one, and an aerial still set")
    func aerialsNotes() throws {
        let pending = MenuModel.rows(try aerials(#"{"event":"aerials","status":{"state":"on"}}"#, pending: true))
        #expect(pending.contains(.aerials(state: "asking…", switchable: false)))
        let queued = MenuModel.rows(try aerials(#"{"event":"aerials","status":{"state":"on"}}"#, pending: true, queued: true))
        #expect(queued.contains(.aerials(state: "waiting…", switchable: false)))

        // A failed switch whose read afterwards failed too still says so.
        let unread = MenuModel.rows(try aerials(#"{"event":"aerials","status":{"state":"unknown","set_for":["wallpaper"]}}"#, failure: "Aerial downloads did not switch: the root step failed"))
        #expect(!unread.contains { if case .aerials = $0 { return true } else { return false } })
        #expect(unread.contains(.note("Aerial downloads did not switch: the root step failed")))
        #expect(unread.contains(.openAerialPane(surface: "wallpaper")))

        let failed = MenuModel.rows(try aerials(#"{"event":"aerials","status":{"state":"on"}}"#, failure: "Aerial downloads did not switch: canceled"))
        #expect(failed.contains(.note("Aerial downloads did not switch: canceled")))

        let set = MenuModel.rows(try aerials(#"{"event":"aerials","status":{"state":"off","set_for":["wallpaper","screen saver"]}}"#))
        let warning = try #require(set.firstIndex(of: .note("An aerial is still set as your wallpaper, so macOS may download for it again: choose another in System Settings ▸ Wallpaper.")))
        #expect(set[warning + 1] == .openAerialPane(surface: "wallpaper"))
        let saver = try #require(set.firstIndex(of: .note("An aerial is still set as your screen saver, so macOS may download for it again: choose another in System Settings ▸ Screen Saver.")))
        #expect(set[saver + 1] == .openAerialPane(surface: "screen saver"))
    }

    private func switchEvent(_ line: String) throws -> Wire.Aerials {
        try #require(Wire.decode(Wire.Aerials.self, from: line))
    }

    @Test("a failed switch's note stays through reads of the same state or none, and goes once the state changes")
    func aerialsFailureKept() throws {
        var aerials = AerialsSwitch(status: Wire.AerialsStatus(state: "on"))
        aerials.start(queued: true)
        #expect(aerials.pending && aerials.queued)
        aerials.switched(try switchEvent(#"{"event":"aerials","status":{"state":"on"},"result":{"ran":true,"before":{"state":"on"},"after":{"state":"on"}}}"#))
        aerials.failed("the root step failed")
        #expect(!aerials.pending && !aerials.queued)
        #expect(aerials.failure == "Aerial downloads are still on: the root step failed")
        aerials.read(Wire.AerialsStatus(state: "on"))
        aerials.read(Wire.AerialsStatus(state: "unknown"))
        #expect(aerials.failure == "Aerial downloads are still on: the root step failed")
        aerials.read(Wire.AerialsStatus(state: "off"))
        #expect(aerials.failure == nil)
        aerials.end(failure: "The engine stopped")
        aerials.read(Wire.AerialsStatus(state: "off"))
        #expect(aerials.failure == "The engine stopped")
        aerials.start(queued: false)
        #expect(aerials.failure == nil)
    }

    @Test("a failed switch's words follow the engine's reads around its root step, not the app's last read")
    func aerialsFailedWords() throws {
        // The picks changed in System Settings since the app last read them.
        var outside = AerialsSwitch(status: Wire.AerialsStatus(state: "off"))
        outside.start(queued: false)
        outside.switched(try switchEvent(#"{"event":"aerials","status":{"state":"on"},"result":{"ran":false,"before":{"state":"on"},"after":{"state":"on"}}}"#))
        outside.failed("canceled at the password prompt; nothing changed")
        #expect(outside.failure == "Aerial downloads did not switch: canceled at the password prompt; nothing changed")

        var changed = AerialsSwitch(status: Wire.AerialsStatus(state: "on"))
        changed.start(queued: false)
        changed.switched(try switchEvent(#"{"event":"aerials","status":{"state":"off"},"result":{"ran":true,"before":{"state":"on"},"after":{"state":"off"}}}"#))
        changed.failed("oplog append failed after the root step: disk full")
        #expect(changed.failure == "Aerial downloads are off now, but the switch ended with an error: oplog append failed after the root step: disk full")

        var unread = AerialsSwitch(status: Wire.AerialsStatus(state: "off"))
        unread.start(queued: false)
        unread.switched(try switchEvent(#"{"event":"aerials","status":{"state":"unknown"},"result":{"ran":true,"before":{"state":"off"},"after":{"state":"unknown"}}}"#))
        unread.failed("the picks are back, but could not be read afterwards to count any not put back: database is locked")
        #expect(unread.failure == "Aerial downloads may have switched: the picks are back, but could not be read afterwards to count any not put back: database is locked")

        // A step the stop may not have ended, the state as it was.
        var stopped = AerialsSwitch(status: Wire.AerialsStatus(state: "on"))
        stopped.start(queued: false)
        stopped.switched(try switchEvent(#"{"event":"aerials","status":{"state":"on"},"result":{"ran":true,"before":{"state":"on"},"after":{"state":"on"}}}"#))
        stopped.failed("stopped before the root step ended; it may still finish")
        #expect(stopped.failure == "Aerial downloads are still on: stopped before the root step ended; it may still finish")

        // An on stopped once its picks were back counts what it could not put back.
        var back = AerialsSwitch(status: Wire.AerialsStatus(state: "off"))
        back.start(queued: false)
        back.switched(try switchEvent(#"{"event":"aerials","status":{"state":"on"},"result":{"ran":true,"before":{"state":"off"},"after":{"state":"on"},"missing":1}}"#))
        back.failed("stopped before the root step ended; it may still finish")
        #expect(back.failure == "Aerial downloads are on now, but the switch ended with an error: stopped before the root step ended; it may still finish. 1 recorded pick is no longer in macOS's aerial catalogue and was not put back.")

        var alone = AerialsSwitch(status: Wire.AerialsStatus(state: "on"))
        alone.start(queued: false)
        alone.failed("busy")
        #expect(alone.failure == "Aerial downloads did not switch: busy")
    }

    @Test("an on that could not put every recorded pick back says how many, as a note")
    func aerialsMissing() throws {
        let event = try switchEvent(#"{"event":"aerials","status":{"state":"off"},"result":{"ran":true,"missing":3}}"#)
        #expect(event.result?.missing == 3)
        #expect(try switchEvent(#"{"event":"aerials","status":{"state":"off"}}"#).result == nil)
        var aerials = AerialsSwitch(status: Wire.AerialsStatus(state: "off"))
        aerials.start(queued: false)
        aerials.switched(event)
        aerials.done()
        #expect(aerials.failure == nil)
        #expect(aerials.note == "3 recorded picks are no longer in macOS's aerial catalogue and were not put back.")
        var state = state(tick: nil)
        state.aerials = aerials
        #expect(MenuModel.rows(state).contains(.note("3 recorded picks are no longer in macOS's aerial catalogue and were not put back.")))
        aerials.read(Wire.AerialsStatus(state: "off"))
        #expect(aerials.note != nil)
        aerials.read(Wire.AerialsStatus(state: "on"))
        #expect(aerials.note == nil)
        #expect(MenuText.aerialsMissing(1) == "1 recorded pick is no longer in macOS's aerial catalogue and was not put back.")
    }

    @Test("with Start with Mac in the menu, the aerials switch sits under it")
    func aerialsUnderLoginItem() throws {
        var state = try aerials(#"{"event":"aerials","status":{"state":"on"}}"#)
        state.loginItem = .on
        let rows = MenuModel.rows(state)
        let index = try #require(rows.firstIndex(of: .aerials(state: "on", switchable: true)))
        #expect(rows[index - 1] == .loginItem(.on))
    }

    @Test("each surface's pane opens by its System Settings link")
    func aerialPanes() {
        #expect(MenuText.aerialPane("wallpaper") == "Wallpaper")
        #expect(MenuText.aerialPaneURL("wallpaper").absoluteString == "x-apple.systempreferences:com.apple.Wallpaper-Settings.extension")
        #expect(MenuText.aerialPane("screen saver") == "Screen Saver")
        #expect(MenuText.aerialPaneURL("screen saver").absoluteString == "x-apple.systempreferences:com.apple.ScreenSaver-Settings.extension")
    }
}
