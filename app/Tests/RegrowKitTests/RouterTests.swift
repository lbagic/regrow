import Foundation
import RegrowKit
import Testing

@Suite("Request queue and routing")
struct RouterTests {
    private let hello = #"{"event":"hello","protocol":1,"version":"0.0.0-dev","fda":"denied"}"#

    private func started() -> Router {
        var router = Router()
        _ = router.engineLine(Data(hello.utf8))
        return router
    }

    private func page(_ router: inout Router, _ fields: [String: Any]) -> [Router.Output] {
        router.pageMessage(fields)
    }

    private func engine(_ router: inout Router, _ line: String) -> [Router.Output] {
        router.engineLine(Data(line.utf8))
    }

    private func tick(_ id: String, autotrim: Bool = false) -> [String: Any] {
        ["type": "tick", "id": id, "autotrim": autotrim]
    }

    private func refusal(_ outputs: [Router.Output]) -> Wire.Head? {
        guard outputs.count == 1, case .page(let line) = outputs[0] else { return nil }
        return Wire.decode(Wire.Head.self, from: line)
    }

    private let runTick = #"{"autotrim":false,"id":"shell-run-tick-1","type":"tick"}"#

    @Test("a run's end sends a tick without autotrim before anything waiting, the shell's ticks and the page's scan")
    func tickAfterARun() {
        var router = started()
        _ = page(&router, ["type": "execute", "id": "x1", "plan_id": "p"])
        _ = router.shellRequest(tick("shell-tick-1", autotrim: true))
        _ = page(&router, ["type": "scan", "id": "s1"])

        let ran = #"{"event":"done","re":"x1","result":{},"canceled":false}"#
        #expect(engine(&router, ran) == [.page(ran), .engine(runTick)])
        let ticked = #"{"event":"done","re":"shell-run-tick-1"}"#
        #expect(engine(&router, ticked) == [.shell(ticked), .engine(#"{"autotrim":true,"id":"shell-tick-1","type":"tick"}"#)])
        let watched = #"{"event":"done","re":"shell-tick-1"}"#
        #expect(engine(&router, watched) == [.shell(watched), .engine(#"{"id":"s1","type":"scan"}"#)])
    }

    @Test("a run that fails, or ends with no page to hear it, is followed by its tick")
    func tickAfterAnyEnd() {
        var router = started()
        _ = page(&router, ["type": "execute", "id": "x1", "plan_id": "gone"])
        let failed = #"{"event":"error","re":"x1","code":"unknown_plan","message":"unknown plan"}"#
        #expect(engine(&router, failed) == [.page(failed), .engine(runTick)])

        var orphan = started()
        _ = page(&orphan, ["type": "execute", "id": "x1", "plan_id": "p"])
        _ = orphan.pageClosed()
        _ = page(&orphan, ["type": "scan", "id": "s1"])
        #expect(engine(&orphan, #"{"event":"done","re":"x1","result":{},"canceled":false}"#) == [.engine(runTick)])
    }

    @Test("a run, or its tick, that ends with the engine is followed by a tick before anything else on the next hello")
    func tickAfterTheEngine() {
        for owed in ["execute", "tick"] {
            var router = started()
            _ = page(&router, ["type": "execute", "id": "x1", "plan_id": "p"])
            if owed == "tick" {
                _ = engine(&router, #"{"event":"done","re":"x1","result":{},"canceled":false}"#)
            }
            _ = router.engineExited(message: "exited with status 2")
            _ = router.shellRequest(tick("shell-tick-1", autotrim: true))
            let outputs = router.engineLine(Data(hello.utf8))
            let id = owed == "execute" ? 1 : 2
            #expect(outputs.last == .engine(#"{"autotrim":false,"id":"shell-run-tick-\#(id)","type":"tick"}"#), "\(owed): \(outputs)")
            #expect(!outputs.contains(.engine(#"{"autotrim":true,"id":"shell-tick-1","type":"tick"}"#)), "\(owed): the watch's tick waits")
        }

        var quiet = started()
        _ = quiet.engineExited(message: "exited with status 2")
        #expect(!quiet.engineLine(Data(hello.utf8)).contains { if case .engine = $0 { return true } else { return false } })
    }

    @Test("a tick waits behind a scan and goes out on the scan's done")
    func tickBehindScan() {
        var router = started()
        #expect(page(&router, ["type": "scan", "id": "s1"]) == [.engine(#"{"id":"s1","type":"scan"}"#)])
        #expect(router.shellRequest(tick("shell-tick-1", autotrim: true)) == [])

        let finding = #"{"event":"finding","re":"s1","scan_id":"a-1","index":0}"#
        #expect(engine(&router, finding) == [.page(finding)])

        let done = #"{"event":"done","re":"s1","elapsed_ms":9,"canceled":false}"#
        #expect(engine(&router, done) == [
            .page(done), .engine(#"{"autotrim":true,"id":"shell-tick-1","type":"tick"}"#),
        ])
    }

    @Test("a scan waits behind a tick and goes out on the tick's done")
    func scanBehindTick() {
        var router = started()
        #expect(router.shellRequest(tick("shell-tick-1")).count == 1)
        #expect(page(&router, ["type": "scan", "id": "s1"]) == [])

        let sample = #"{"event":"tick","re":"shell-tick-1","free":1}"#
        #expect(engine(&router, sample) == [.shell(sample)])

        let done = #"{"event":"done","re":"shell-tick-1"}"#
        #expect(engine(&router, done) == [.shell(done), .engine(#"{"id":"s1","type":"scan"}"#)])
    }

    @Test("an execute waits behind a scan, and the ladder is told while it is in flight")
    func executeBehindScan() {
        var router = started()
        _ = page(&router, ["type": "scan", "id": "s1"])
        #expect(page(&router, ["type": "execute", "id": "x1", "plan_id": "p-1"]) == [])
        #expect(!router.executeInFlight)

        let done = #"{"event":"done","re":"s1","elapsed_ms":9,"canceled":false}"#
        #expect(engine(&router, done) == [
            .page(done), .engine(#"{"id":"x1","plan_id":"p-1","type":"execute"}"#),
        ])
        #expect(router.executeInFlight)

        _ = engine(&router, #"{"event":"done","re":"x1","result":{},"canceled":false}"#)
        #expect(!router.executeInFlight)
    }

    @Test("plan and cancel pass an engine that is busy")
    func planAndCancelPass() {
        var router = started()
        _ = page(&router, ["type": "scan", "id": "s1"])

        #expect(page(&router, ["type": "plan", "id": "p1", "scan_id": "a-1", "select": ["cache"]]) == [
            .engine(#"{"id":"p1","scan_id":"a-1","select":["cache"],"type":"plan"}"#)
        ])
        #expect(page(&router, ["type": "cancel", "id": "c1", "target": "s1"]) == [
            .engine(#"{"id":"c1","target":"s1","type":"cancel"}"#)
        ])
    }

    @Test("a plan's done does not release the slot a scan holds")
    func planDoneKeepsTheSlot() {
        var router = started()
        _ = page(&router, ["type": "scan", "id": "s1"])
        _ = page(&router, ["type": "plan", "id": "p1", "scan_id": "a-0"])
        _ = router.shellRequest(tick("shell-tick-1"))

        let planned = #"{"event":"done","re":"p1"}"#
        #expect(engine(&router, planned) == [.page(planned)])
    }

    @Test("a terminal error releases the slot as a done does")
    func errorReleasesTheSlot() {
        var router = started()
        _ = page(&router, ["type": "scan", "id": "s1"])
        _ = router.shellRequest(tick("shell-tick-1"))

        let failed = #"{"event":"error","re":"s1","code":"busy","message":"a tick is in flight"}"#
        #expect(engine(&router, failed) == [
            .page(failed), .engine(#"{"autotrim":false,"id":"shell-tick-1","type":"tick"}"#),
        ])
    }

    @Test("the page may not tick")
    func pageTickRefused() {
        var router = started()
        let head = refusal(page(&router, ["type": "tick", "id": "t1"]))
        #expect(head?.event == "error")
        #expect(head?.code == "unknown_request")
        #expect(head?.re == "t1")
    }

    @Test("the page may not use an id that starts shell-")
    func pageShellIdRefused() {
        var router = started()
        let head = refusal(page(&router, ["type": "scan", "id": "shell-1"]))
        #expect(head?.code == "bad_request")
        #expect(head?.re == "shell-1")
    }

    @Test("a page request without a string id is refused", arguments: [
        ["type": "scan"] as [String: any Sendable],
        ["type": "scan", "id": 7],
        ["type": "scan", "id": ""],
    ])
    func pageRequestWithoutIdRefused(fields: [String: any Sendable]) {
        var router = started()
        let head = refusal(page(&router, fields))
        #expect(head?.code == "bad_request")
        #expect(head?.re == nil)
    }

    @Test("a page request reusing the id of one still held is refused, so the slot is released by one request only")
    func duplicateIdRefused() {
        var router = started()
        _ = page(&router, ["type": "scan", "id": "s1"])
        #expect(refusal(page(&router, ["type": "plan", "id": "s1", "scan_id": "a-1"]))?.code == "bad_request")
    }

    @Test("a request too long for the engine is refused here, where it still has its id")
    func longRequestRefused() {
        var router = started()
        let atoms = Array(repeating: String(repeating: "a", count: 1024), count: 1100)
        let head = refusal(page(&router, ["type": "plan", "id": "p1", "scan_id": "a-1", "select": atoms]))
        #expect(head?.code == "line_too_long")
        #expect(head?.re == "p1")
    }

    @Test("a message that is no object is refused, and a {shell: …} message is a command")
    func messageShapes() {
        var router = started()
        #expect(refusal(router.pageMessage("scan"))?.code == "bad_request")
        #expect(router.pageMessage(["shell": "ready"]) == [.command("ready")])
    }

    @Test("a copy is its text, whole; one without a text is refused")
    func copyMessages() {
        var router = started()
        let command = #"sudo find "/Library/Application Support/x" -type f -name '*.mov' -delete"#
        #expect(router.pageMessage(["shell": "copy", "text": command]) == [.copy(command)])
        #expect(refusal(router.pageMessage(["shell": "copy"]))?.code == "bad_request")
        #expect(refusal(router.pageMessage(["shell": "copy", "text": ""]))?.code == "bad_request")
        #expect(refusal(router.pageMessage(["shell": "copy", "text": 7]))?.code == "bad_request")
    }

    @Test("events go to the side their re names; hello goes to both")
    func routingByRe() {
        var router = Router()
        #expect(engine(&router, hello) == [.shell(hello), .page(hello)])

        let shells = #"{"event":"tick","re":"shell-tick-4","free":1}"#
        #expect(engine(&router, shells) == [.shell(shells)])

        let pages = #"{"event":"summary","re":"s1","scan_id":"a-1"}"#
        #expect(engine(&router, pages) == [.page(pages)])

        let unaddressed = #"{"event":"error","code":"bad_request","message":"not JSON"}"#
        #expect(engine(&router, unaddressed) == [.page(unaddressed)])

        #expect(engine(&router, "not an event") == [])
    }

    @Test("requests sent before the engine's hello go out when it arrives")
    func heldUntilHello() {
        var router = Router()
        #expect(page(&router, ["type": "scan", "id": "s1"]) == [])
        #expect(page(&router, ["type": "cancel", "id": "c1", "target": "s0"]) == [])
        #expect(engine(&router, hello) == [
            .shell(hello), .page(hello),
            .engine(#"{"id":"c1","target":"s0","type":"cancel"}"#),
            .engine(#"{"id":"s1","type":"scan"}"#),
        ])
    }

    @Test("when the engine exits, what was in flight or waiting is dropped and both sides hear it")
    func droppedOnExit() throws {
        var router = started()
        _ = page(&router, ["type": "scan", "id": "s1"])
        _ = router.shellRequest(tick("shell-tick-1"))
        _ = page(&router, ["type": "execute", "id": "x1", "plan_id": "p-1"])

        let outputs = router.engineExited(message: "the engine exited with status 2")
        let down = #"{"event":"shell","message":"the engine exited with status 2","state":"engine_down"}"#
        #expect(outputs == [.shell(down), .page(down)])
        #expect(!router.executeInFlight)

        #expect(engine(&router, hello) == [.shell(hello), .page(hello)])
        #expect(page(&router, ["type": "scan", "id": "s2"]) == [.engine(#"{"id":"s2","type":"scan"}"#)])
    }

    @Test("a cancel of a scan still waiting here drops it and answers both, as the engine answers a canceled scan")
    func cancelOfWaitingScan() {
        var router = started()
        _ = router.shellRequest(tick("shell-tick-1"))
        _ = page(&router, ["type": "scan", "id": "s1"])

        #expect(page(&router, ["type": "cancel", "id": "c1", "target": "s1"]) == [
            .page(#"{"canceled":true,"elapsed_ms":0,"event":"done","re":"s1"}"#),
            .page(#"{"event":"done","re":"c1"}"#),
        ])
        let done = #"{"event":"done","re":"shell-tick-1"}"#
        #expect(engine(&router, done) == [.shell(done)])
    }

    @Test("a cancel of an execute still waiting here means nothing ran")
    func cancelOfWaitingExecute() throws {
        var router = started()
        _ = router.shellRequest(tick("shell-tick-1"))
        _ = page(&router, ["type": "execute", "id": "x1", "plan_id": "p-1"])

        let outputs = page(&router, ["type": "cancel", "id": "c1", "target": "x1"])
        try #require(outputs.count == 2)
        guard case .page(let line) = outputs[0] else {
            Issue.record("the execute's answer goes to the page")
            return
        }
        let answer = try #require(
            JSONSerialization.jsonObject(with: Data(line.utf8)) as? [String: Any])
        let result = try #require(answer["result"] as? [String: Any])
        #expect(answer["event"] as? String == "done")
        #expect(answer["re"] as? String == "x1")
        #expect(answer["canceled"] as? Bool == true)
        #expect(result["run_id"] as? String == "p-1")
        #expect(result["done"] as? Int == 0)
        #expect(result["stopped"] as? Bool == true)
        #expect(outputs[1] == .page(#"{"event":"done","re":"c1"}"#))
    }

    @Test("the page cannot drop a tick of the shell's: its cancel goes to the engine")
    func pageCannotDropAShellTick() {
        var router = started()
        _ = page(&router, ["type": "scan", "id": "s1"])
        _ = router.shellRequest(tick("shell-tick-1"))
        #expect(page(&router, ["type": "cancel", "id": "c1", "target": "shell-tick-1"]) == [
            .engine(#"{"id":"c1","target":"shell-tick-1","type":"cancel"}"#)
        ])
    }

    @Test("when the page closes, its waiting requests are dropped and its scan in flight is canceled")
    func pageClosed() {
        var router = started()
        _ = page(&router, ["type": "scan", "id": "s1"])
        _ = page(&router, ["type": "scan", "id": "s2"])
        #expect(router.pageClosed() == [
            .engine(#"{"id":"shell-cancel-1","target":"s1","type":"cancel"}"#)
        ])

        let done = #"{"event":"done","re":"s1","elapsed_ms":3,"canceled":true}"#
        #expect(engine(&router, done) == [])
    }

    @Test("an execute in flight is left to finish when the page closes; what answers it reaches no later page, and its end frees the slot")
    func pageClosedDuringExecute() {
        var router = started()
        _ = page(&router, ["type": "execute", "id": "x1", "plan_id": "p-1"])
        #expect(router.pageClosed() == [])
        #expect(router.executeInFlight)

        _ = router.shellRequest(tick("shell-tick-1"))
        #expect(engine(&router, #"{"event":"journal","re":"x1","entry":{"seq":1}}"#) == [])
        #expect(engine(&router, #"{"event":"done","re":"x1","result":{},"canceled":false}"#) == [.engine(runTick)])
        #expect(!router.executeInFlight)
        let ticked = #"{"event":"done","re":"shell-run-tick-1"}"#
        #expect(engine(&router, ticked) == [.shell(ticked), .engine(#"{"autotrim":false,"id":"shell-tick-1","type":"tick"}"#)])
    }

    @Test("a page opened later may reuse the id of a scan the earlier page left in flight: it waits for that one's end")
    func idReusedAfterThePageClosed() {
        var router = started()
        _ = page(&router, ["type": "scan", "id": "s1"])
        _ = router.pageClosed()

        #expect(page(&router, ["type": "scan", "id": "s1"]) == [])
        #expect(refusal(page(&router, ["type": "plan", "id": "s1", "scan_id": "a-1"]))?.code == "bad_request")

        let canceled = #"{"event":"done","re":"s1","elapsed_ms":3,"canceled":true}"#
        #expect(engine(&router, canceled) == [.engine(#"{"id":"s1","type":"scan"}"#)])
        let finding = #"{"event":"finding","re":"s1","scan_id":"a-2","index":0}"#
        #expect(engine(&router, finding) == [.page(finding)])
    }
}
