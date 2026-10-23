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
            .separator, .open, .separator, .autotrim(on: false), .separator, .fullDiskAccess("granted"), .separator, .quit,
        ])
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

    @Test("before the first tick the figures say so, and every action is there")
    func noTick() throws {
        let rows = MenuModel.rows(MenuState(
            tick: nil, checkedAt: nil, tickFailure: nil, engineDown: nil, autotrim: false, fda: nil,
            environmentFailure: nil, engine: Self.engine))
        let figures = try #require(figuresOf(rows))
        #expect(figures.free == nil)
        #expect(figures.checked == "Not checked yet")
        #expect(rows.contains(.open))
        #expect(rows.contains(.fullDiskAccess("not checked yet")))
        #expect(!rows.contains(.openFullDiskAccess))
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
            .open, .autotrim(on: true), .copy(Self.engine + " prune go-build --yes"), .openFullDiskAccess, .relaunch,
            .staging(2_254_857_830), .quit,
        ] {
            #expect(rows.contains(action), "\(action)")
        }
        #expect(rows.contains(.fullDiskAccess("not granted")))
        #expect(rows.contains(.environment("Login environment not captured: the login shell gave no answer within 5 s")))
    }
}
