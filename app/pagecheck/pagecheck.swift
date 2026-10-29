// pagecheck draws regrow's scan page in WebKit, the engine the app's web view uses, and plays the shell and
// the engine to it from a recorded transcript.
//
//   swiftc -O -o pagecheck app/pagecheck/pagecheck.swift
//   pagecheck <page.html> <transcript.jsonl> <out dir>
//
// Each scenario loads the page fresh in an offscreen web view, answers what the page asks with the section of
// the transcript the scenario names, waits for the page to settle, asserts on the DOM and on what the page
// sent, and writes a PNG of the 1096×640 window. It exits 1 with a line per failure. Two things in the
// environment are for a closer look: PAGECHECK_ONLY, a comma-separated list of scenario names, runs only
// those; PAGECHECK_SIZE, as 916x520, draws the window at another size.
//
// What reaches the page is the transcript's own lines, with four substitutions a recording cannot hold:
// REQ becomes the id of the request being answered; a plan's expires_at is set from this run's clock, in the
// shape a real engine writes it (a fraction of a second); hello's fda is set to denied for one scenario; and a
// canceled scan ends with the recorded done line saying canceled. Error events and the shell's own messages
// are not in the transcript: the checker writes them, the errors in the engine's words.
//
// Nothing here waits on a guess of how long the page takes. The page says when it has settled; a plan grows
// too old because the checker moves the page's clock forward, not because time passed.
import AppKit
import WebKit

// MARK: - The transcript

extension Dictionary where Key == String, Value == Any {
    func str(_ key: String) -> String { self[key] as? String ?? "" }
    func int(_ key: String) -> Int64 { (self[key] as? NSNumber)?.int64Value ?? 0 }
    func obj(_ key: String) -> [String: Any] { self[key] as? [String: Any] ?? [:] }
    func objs(_ key: String) -> [[String: Any]] { self[key] as? [[String: Any]] ?? [] }
    func strs(_ key: String) -> [String] { self[key] as? [String] ?? [] }
}

func parse(_ line: String) -> [String: Any] {
    (try? JSONSerialization.jsonObject(with: Data(line.utf8))) as? [String: Any] ?? [:]
}

func json(_ object: [String: Any]) -> String {
    String(data: try! JSONSerialization.data(withJSONObject: object, options: [.sortedKeys]), encoding: .utf8)!
}

// engine.HumanBytes
func human(_ n: Int64) -> String {
    if n < 1024 { return "\(n) B" }
    var div: Int64 = 1024, exp = 0, m = n / 1024
    while m >= 1024 { div *= 1024; exp += 1; m /= 1024 }
    return String(format: "%.1f %@iB", Double(n) / Double(div), String(Array("KMGTPE")[exp]))
}

// A command as a shell reads it back, as the page's shellLine writes it for the pasteboard
func shellLine(_ argv: [String]) -> String {
    argv.map { word in
        guard word.range(of: #"[ \t\n'"\\$`*?\[\](){}<>|&;!#]"#, options: .regularExpression) != nil else { return word }
        return "'" + word.replacingOccurrences(of: "'", with: #"'\''"#) + "'"
    }.joined(separator: " ")
}

struct Transcript {
    private(set) var sections: [String: [String]] = [:]

    init(path: String) throws {
        var name = ""
        for line in try String(contentsOfFile: path, encoding: .utf8).split(separator: "\n").map(String.init) {
            let section = parse(line).str("section")
            if section.isEmpty { sections[name, default: []].append(line) } else { name = section }
        }
    }

    func lines(_ section: String) -> [String] { sections[section] ?? [] }
    func events(_ section: String, _ event: String) -> [[String: Any]] {
        lines(section).map(parse).filter { $0.str("event") == event }
    }
    func event(_ section: String, _ event: String) -> [String: Any] { events(section, event).first ?? [:] }

    /// The findings of a scan that the page draws as a row: those with an item or an error.
    func rows(_ section: String) -> [[String: Any]] {
        events(section, "finding").filter { !$0.obj("finding").objs("items").isEmpty || !$0.obj("finding").str("error").isEmpty }
    }
    /// Every atom a finding's select of this scan holds.
    func atoms(_ section: String) -> Set<String> { Set(events(section, "finding").flatMap { $0.strs("select") }) }
    /// What a plan's select ticks: a rule id there stands for every atom of that rule's row.
    func ticked(by plan: String, in scan: String) -> Set<String> {
        let select = Set(event(plan, "plan").strs("select"))
        var out = Set<String>()
        for finding in events(scan, "finding") {
            let rule = finding.obj("finding").obj("rule").str("id")
            for atom in finding.strs("select") where select.contains(atom) || select.contains(rule) { out.insert(atom) }
        }
        return out
    }
}

// MARK: - What the shell and the engine do in a scenario

struct Play {
    var fdaDenied = false
    var fdaWord: String?                 // the hello's fda in place of the recorded one, as fdaDenied does for denied
    var tick: String? = "tick"           // the section whose tick the shell relays; nil when it has none yet
    var environment: String?             // the reason the shell could not read the login environment
    var scan = "scan"
    var scanStops = false                // half the findings arrive, the rest wait for a cancel
    var planError: (code: String, message: String)?  // the answer to the next plan request that carries a select
    var leaveOpen = false                // a selection the transcript has no plan for goes unanswered
    var holdPlan = false                 // the next plan with a select is answered only by releasePlan(), in the order held
    var automation: Bool? = true         // nil: the answer is withheld
    var execute = "execute"
    var executeStops = false             // half the journal arrives
    var executeLines: Int?               // only this many lines of the journal arrive
    var commandsOnly = false             // plans keep only their commands, no Trash move
    var helloSelf: String?               // the hello's self, in place of the recorded one
    var executeError: (code: String, message: String)?  // the answer to the next execute
    var clockStops = false               // the page's timers never fire
    var opened = false                   // the window was opened with Open: the shell asks for no scan
    var scanFirst = false                // the shell asks for a scan before it hands over the engine's hello
    var holdHello = false                // the engine has not said hello when the page is ready
    var settings: [String: Any]? = Play.installed  // the shell's settings event; nil: it has read none yet
    var tickAfterRun: String?            // the section whose tick the shell relays once a run ends, as the app does
    var historyEvent: [String: Any]?     // the history event in place of the transcript's
    var historyError: String?            // the history request fails with this message
    var holdHistory = false              // history requests wait in heldHistory until the scenario answers them

    /// The settings of the installed app on a Mac that has granted it all but Control of Finder.
    static let installed: [String: Any] = [
        "event": "shell", "state": "settings", "login_item": "on", "notifications": "allowed", "finder": "not_asked",
        "log": "/Users/dev/Library/Logs/regrow/app.log", "staging": "/Users/dev/.local/state/regrow/staging",
    ]
}

/// The window the page is drawn in: the size DESIGN.md draws it for, unless the environment asks otherwise.
let windowSize: CGSize = {
    let parts = (ProcessInfo.processInfo.environment["PAGECHECK_SIZE"] ?? "").split(separator: "x").compactMap { Double($0) }
    return parts.count == 2 ? CGSize(width: parts[0], height: parts[1]) : CGSize(width: 1096, height: 640)
}()

@MainActor
final class Run: NSObject, WKNavigationDelegate, WKUIDelegate, WKScriptMessageHandler {
    let name: String
    let transcript: Transcript
    let out: URL
    var play: Play
    var heldHistory: [String] = []

    private let webView: WKWebView
    private let window: NSWindow
    private(set) var failures: [String] = []
    private(set) var checks = 0
    private(set) var shots: [String] = []

    /// Every message the page posted, in order.
    private(set) var posted: [[String: Any]] = []
    /// What the copies put on the pasteboard, read back from it: the shell's two calls, on a pasteboard of
    /// this checker's own, so the owner's is left as it was.
    private(set) var pasted: [String] = []
    private let pasteboard = NSPasteboard(name: NSPasteboard.Name("com.github.lbagic.regrow.pagecheck"))
    private var outbox: [String] = []
    private var pumping = false
    private var loads = 0
    private var ids = Set<String>()

    private var ahead = 0.0              // seconds the page's clock runs ahead of this one
    private var given = Set<String>()    // atoms the delivered findings' selects handed over
    private var scanRequest = ""
    private var scanHeld: [String] = []
    private var scanID = ""
    private(set) var planID = ""
    private(set) var executeRequest = ""
    private var planHeld: [[String]] = []

    init(name: String, dark: Bool, transcript: Transcript, out: URL, play: Play) {
        self.name = name + (dark ? "-dark" : "-light")
        self.transcript = transcript
        self.out = out
        self.play = play
        let frame = NSRect(origin: .zero, size: windowSize)
        let configuration = WKWebViewConfiguration()
        configuration.websiteDataStore = .nonPersistent()
        // From the document's first line on: script errors, rejections, and anything the page's policy refused.
        // In a function, so it adds no name to the page's own.
        configuration.userContentController.addUserScript(WKUserScript(
            source: """
                (function () {
                  const tell = (text) => window.webkit.messageHandlers.pagecheck.postMessage(text);
                  addEventListener('error', (e) => tell('script error: ' + e.message + ' (line ' + e.lineno + ')'));
                  addEventListener('unhandledrejection', (e) => tell('unhandled rejection: ' + e.reason));
                  addEventListener('securitypolicyviolation', (e) => tell('refused by the page policy: ' + e.violatedDirective + ' ' + e.blockedURI));
                })();
                """,
            injectionTime: .atDocumentStart, forMainFrameOnly: true))
        // The page's clock can be moved forward, and for one scenario its timers never fire. Each Return key
        // that has gone all the way through the page is counted, so a key press is waited for, not guessed at.
        configuration.userContentController.addUserScript(WKUserScript(
            source: """
                (function () {
                  const now = Date.now.bind(Date);
                  window.pagecheckAhead = 0;
                  Date.now = () => now() + window.pagecheckAhead;
                  if (\(play.clockStops)) window.setInterval = () => 0;
                  window.pagecheckKeys = 0;
                  addEventListener('keyup', (e) => { if (e.key === 'Enter') window.pagecheckKeys++; }, true);
                })();
                """,
            injectionTime: .atDocumentStart, forMainFrameOnly: true))
        webView = WKWebView(frame: frame, configuration: configuration)
        window = NSWindow(contentRect: frame, styleMask: [.borderless], backing: .buffered, defer: false)
        super.init()
        webView.configuration.userContentController.add(self, name: "regrow")
        webView.configuration.userContentController.add(self, name: "pagecheck")
        // Under ARC a window that releases itself when closed is released twice.
        window.isReleasedWhenClosed = false
        window.appearance = NSAppearance(named: dark ? .darkAqua : .aqua)
        window.contentView = webView
        window.setFrameOrigin(NSPoint(x: -20000, y: -20000))
        window.orderBack(nil)
        window.makeFirstResponder(webView)
        webView.navigationDelegate = self
        webView.uiDelegate = self
    }

    func close() {
        webView.configuration.userContentController.removeAllScriptMessageHandlers()
        webView.navigationDelegate = nil
        window.close()
    }

    // MARK: checks

    func expect(_ ok: Bool, _ what: @autoclosure () -> String) {
        checks += 1
        if !ok { fail(what()) }
    }

    func fail(_ what: String) { failures.append("\(name): \(what)") }

    func same<T: Equatable>(_ got: T, _ want: T, _ what: String) { expect(got == want, "\(what): got \(got), want \(want)") }

    func has(_ text: String, _ part: String, _ what: String) { expect(text.contains(part), "\(what): \"\(text)\" lacks \"\(part)\"") }

    // MARK: the page

    func load(_ page: URL) async {
        webView.loadFileURL(page, allowingReadAccessTo: page)
        await settle()
    }

    /// Any navigation after the first load is the page reaching for something outside the bridge.
    func webView(_ webView: WKWebView, decidePolicyFor action: WKNavigationAction, decisionHandler: @escaping (WKNavigationActionPolicy) -> Void) {
        loads += 1
        if loads == 1 {
            decisionHandler(.allow)
        } else {
            fail("the page navigated to \(action.request.url?.absoluteString ?? "nowhere")")
            decisionHandler(.cancel)
        }
    }

    func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: Error) {
        fail("the page did not load: \(error.localizedDescription)")
    }

    func webView(_ webView: WKWebView, createWebViewWith configuration: WKWebViewConfiguration, for action: WKNavigationAction, windowFeatures: WKWindowFeatures) -> WKWebView? {
        fail("the page tried to open a window")
        return nil
    }

    /// Runs a function body in the page and returns what it returns, by way of JSON.
    @discardableResult
    func js(_ body: String, _ arguments: [String: Any] = [:]) async -> Any? {
        do {
            let value = try await webView.callAsyncJavaScript(
                "return JSON.stringify((() => { \(body) })())", arguments: arguments, in: nil, contentWorld: .page)
            guard let text = value as? String else { return nil }
            return try JSONSerialization.jsonObject(with: Data(text.utf8), options: [.fragmentsAllowed])
        } catch {
            fail("could not run \"\(body.prefix(60))\": \(error.localizedDescription)")
            return nil
        }
    }

    /// What an element says, without the copy controls beside its commands.
    private static let words = """
        const words = (el) => { const c = el.cloneNode(true); c.querySelectorAll('.copy').forEach((b) => b.remove()); return c.textContent.replace(/\\s+/g, ' ').trim(); };
        """

    func text(_ selector: String) async -> String {
        await js(Self.words + "const el = document.querySelector(selector); return el ? words(el) : ''", ["selector": selector]) as? String ?? ""
    }

    func texts(_ selector: String) async -> [String] {
        await js(Self.words + "return [...document.querySelectorAll(selector)].map(words)", ["selector": selector]) as? [String] ?? []
    }

    func count(_ selector: String) async -> Int {
        await js("return document.querySelectorAll(selector).length", ["selector": selector]) as? Int ?? -1
    }

    func flag(_ body: String) async -> Bool { await js(body) as? Bool ?? false }

    func screen() async -> String { await js("return document.documentElement.dataset.screen || ''") as? String ?? "" }
    func phase() async -> String { await js("return document.documentElement.dataset.scan || ''") as? String ?? "" }
    func paper() async -> String { await js("return getComputedStyle(document.body).backgroundColor") as? String ?? "" }

    /// Clicks an element as script can: `force` first takes away what marks it as disabled, so that only the
    /// page's own check of its state stands between the click and what it would do.
    @discardableResult
    func click(_ selector: String, force: Bool = false) async -> Bool {
        let hit = await js("""
            const el = document.querySelector(selector);
            if (!el) return false;
            if (force) { el.disabled = false; el.removeAttribute('aria-disabled'); }
            el.click();
            return true;
            """, ["selector": selector, "force": force]) as? Bool ?? false
        await settle()
        return hit
    }

    func focus(_ selector: String) async {
        await js("const el = document.querySelector(selector); if (el) el.focus(); return null", ["selector": selector])
    }

    /// A real Return key, through the web view as a keyboard would send it.
    func pressReturn() async {
        let before = await js("return window.pagecheckKeys") as? Int ?? 0
        for type in [NSEvent.EventType.keyDown, .keyUp] {
            guard let event = NSEvent.keyEvent(
                with: type, location: .zero, modifierFlags: [], timestamp: ProcessInfo.processInfo.systemUptime,
                windowNumber: window.windowNumber, context: nil, characters: "\r", charactersIgnoringModifiers: "\r",
                isARepeat: false, keyCode: 36)
            else { continue }
            if type == .keyDown { webView.keyDown(with: event) } else { webView.keyUp(with: event) }
        }
        // A key reaches the page by another road than a call does.
        await wait(for: "the Return key reaching the page", "window.pagecheckKeys > \(before)")
    }

    func sleep(_ seconds: Double) async { try? await Task.sleep(nanoseconds: UInt64(seconds * 1e9)) }

    /// Waits until the page has drawn everything it was sent and says it has settled: nothing is left to
    /// deliver, the page's mark is set, and neither the mark nor what the page has posted changes any more.
    /// The mark is read by a call into the page, and what the page posted before answering that call has
    /// arrived by the time it returns, so the short pause is a margin, not what the wait rests on.
    func settle() async {
        let deadline = Date().addingTimeInterval(45)
        var seen = ("", -1), since = Date()
        while Date() < deadline {
            let mark = await js("return document.documentElement.dataset.settled || ''") as? String ?? ""
            if (mark, posted.count) != seen || mark.isEmpty || pumping || !outbox.isEmpty {
                seen = (mark, posted.count)
                since = Date()
            } else if Date().timeIntervalSince(since) > 0.08 {
                return
            }
            await sleep(0.02)
        }
        fail("the page did not settle")
    }

    /// Waits for something the page does by its own clock.
    func wait(for what: String, _ condition: String) async {
        let deadline = Date().addingTimeInterval(45)
        while Date() < deadline {
            if await flag("return " + condition) { return await settle() }
            await sleep(0.05)
        }
        fail("never happened: \(what)")
    }

    /// Moves the page's clock forward.
    func jump(minutes: Double) async {
        ahead += minutes * 60
        await js("window.pagecheckAhead = ms; return null", ["ms": ahead * 1000])
    }

    func shoot(_ suffix: String = "", top selector: String? = nil) async {
        if let selector {
            await js("const el = document.querySelector(selector); if (el) document.getElementById('page').scrollTop += el.getBoundingClientRect().top - 110; return null", ["selector": selector])
            await settle()
        }
        let configuration = WKSnapshotConfiguration()
        configuration.rect = CGRect(origin: .zero, size: windowSize)
        let file = out.appendingPathComponent(name.replacingOccurrences(of: "-light", with: suffix + "-light").replacingOccurrences(of: "-dark", with: suffix + "-dark") + ".png")
        do {
            let image = try await webView.takeSnapshot(configuration: configuration)
            guard let tiff = image.tiffRepresentation, let bitmap = NSBitmapImageRep(data: tiff),
                let png = bitmap.representation(using: .png, properties: [:])
            else { return fail("the snapshot could not be encoded") }
            try png.write(to: file)
            shots.append(file.path)
        } catch {
            fail("no snapshot: \(error.localizedDescription)")
        }
    }

    // MARK: the shell and the engine

    /// The requests of one type the page has sent.
    func sent(_ type: String) -> [[String: Any]] { posted.filter { $0.str("type") == type } }
    func sentShell(_ what: String) -> Int { posted.filter { $0.str("shell") == what }.count }

    func send(_ lines: [String]) {
        outbox.append(contentsOf: lines)
        if pumping { return }
        pumping = true
        Task { await pump() }
    }

    func send(_ event: [String: Any]) { send([json(event)]) }

    /// One line at a time, as the app hands them over, with a gap so a scan is seen arriving.
    private func pump() async {
        while !outbox.isEmpty {
            let line = outbox.removeFirst()
            if parse(line).str("event") == "finding" { given.formUnion(parse(line).strs("select")) }
            do {
                _ = try await webView.callAsyncJavaScript(
                    "window.regrow.receive(line); return true", arguments: ["line": line], in: nil, contentWorld: .page)
            } catch {
                fail("the page could not take an event: \(error.localizedDescription)")
            }
            await sleep(0.008)
        }
        pumping = false
    }

    func answering(_ section: String, _ id: String) -> [String] {
        transcript.lines(section).map { line in
            if !line.contains("\"re\":\"REQ\"") { fail("a line of \(section) has no REQ to replace") }
            return line.replacingOccurrences(of: "\"re\":\"REQ\"", with: "\"re\":\(json(["v": id]).dropFirst(5).dropLast(1))")
        }
    }

    func userContentController(_ controller: WKUserContentController, didReceive message: WKScriptMessage) {
        if message.name == "pagecheck" { return fail("\(message.body)") }
        guard let body = message.body as? [String: Any] else { return fail("the page posted \(message.body)") }
        posted.append(body)
        let shell = body.str("shell")
        if !shell.isEmpty { return answer(shell: shell, body) }
        let id = body.str("id")
        expect(!id.isEmpty && !id.hasPrefix("shell-") && ids.insert(id).inserted, "a request needs an id of its own that does not start with shell-: \(body)")
        switch body.str("type") {
        case "scan": answer(scan: id)
        case "plan": answer(plan: id, body)
        case "execute": answer(execute: id, body)
        case "cancel": answer(cancel: id, body)
        case "history":
            if play.holdHistory {
                heldHistory.append(id)
            } else if let message = play.historyError {
                send(["event": "error", "re": id, "code": "history_failed", "message": message])
            } else if var event = play.historyEvent {
                event["re"] = id
                send(event)
                send(["event": "done", "re": id])
            } else {
                send(answering("history", id))
            }
        default: fail("the page sent a request the shell refuses: \(body)")
        }
    }

    private func answer(shell: String, _ body: [String: Any]) {
        switch shell {
        case "copy":
            pasteboard.clearContents()
            pasteboard.setString(body.str("text"), forType: .string)
            pasted.append(pasteboard.string(forType: .string) ?? "")
        case "ready":
            var hello = transcript.lines("hello")[0]
            if let word = play.fdaWord ?? (play.fdaDenied ? "denied" : nil) {
                expect(hello.contains("\"fda\":\"granted\""), "the recorded hello has no fda to replace")
                hello = hello.replacingOccurrences(of: "\"fda\":\"granted\"", with: "\"fda\":\"\(word)\"")
            }
            if let path = play.helloSelf {
                var event = parse(hello)
                event["self"] = path
                hello = json(event)
            }
            if play.scanFirst { send(["event": "shell", "state": "scan"]) }
            if !play.holdHello { send([hello]) }
            if let tick = play.tick { send([answering(tick, "shell-1")[0]]) }
            if let reason = play.environment { send(["event": "shell", "state": "environment", "captured": false, "reason": reason]) }
            if let settings = play.settings { send(settings) }
            if !play.opened && !play.scanFirst { send(["event": "shell", "state": "scan"]) }
        case "automation":
            if let allowed = play.automation { send(["event": "shell", "state": "automation", "allowed": allowed]) }
        case "open-full-disk-access", "relaunch", "login-item-on", "login-item-off", "login-item-settings", "notification-settings",
            "automation-settings", "show-log", "show-staging":
            break
        default:
            fail("the page sent a shell message the bridge does not have: \(shell)")
        }
    }

    private func answer(scan id: String) {
        scanRequest = id
        scanID = transcript.event(play.scan, "start").str("scan_id")
        given = []
        let lines = answering(play.scan, id)
        if play.scanStops {
            let findings = lines.indices.filter { parse(lines[$0]).str("event") == "finding" }
            let cut = findings[findings.count / 2]
            scanHeld = Array(lines[cut...])
            send(Array(lines[..<cut]))
        } else {
            send(lines)
        }
    }

    private func answer(plan id: String, _ body: [String: Any]) {
        same(body.str("scan_id"), scanID, "the plan request's scan_id")
        let select = body["select"] as? [String]
        for atom in select ?? [] { expect(given.contains(atom), "the page sent an atom no select gave it: \(atom)") }
        if select != nil, let error = play.planError {
            play.planError = nil
            return send(["event": "error", "re": id, "code": error.code, "message": error.message])
        }
        var section = "plan-default"
        if let select {
            if Set(select) == transcript.atoms(play.scan) {
                section = "plan-all"
            } else if Set(select) != transcript.ticked(by: "plan-default", in: play.scan) {
                if !play.leaveOpen { fail("the transcript holds no plan for a selection of \(select.count) atoms") }
                return
            }
        }
        // A real engine writes the expiry with a fraction of a second.
        let stamp = ISO8601DateFormatter()
        stamp.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        let expires = stamp.string(from: Date().addingTimeInterval(ahead + 570)).replacingOccurrences(of: "Z", with: "081Z")
        var lines = answering(section, id)
        lines[0] = lines[0].replacingOccurrences(of: "\"expires_at\":\"[^\"]*\"", with: "\"expires_at\":\"\(expires)\"", options: .regularExpression)
        if !lines[0].contains(expires) { fail("the recorded plan has no expires_at to set") }
        if play.commandsOnly {
            var event = parse(lines[0]), plan = event.obj("plan")
            plan["actions"] = plan.objs("actions").filter { $0.str("kind") != "trash" }
            event["plan"] = plan
            lines[0] = json(event)
        }
        if select != nil, play.holdPlan {
            play.holdPlan = false
            planHeld.append(lines)
            return
        }
        planID = parse(lines[0]).str("plan_id")
        send(lines)
    }

    /// Sends the oldest answer `holdPlan` kept back, late.
    func releasePlan() {
        expect(!planHeld.isEmpty, "no plan answer was held")
        guard !planHeld.isEmpty else { return }
        let lines = planHeld.removeFirst()
        planID = parse(lines[0]).str("plan_id")
        send(lines)
    }

    private func answer(execute id: String, _ body: [String: Any]) {
        executeRequest = id
        same(body.str("plan_id"), planID, "the execute's plan_id")
        if let error = play.executeError {
            play.executeError = nil
            return send(["event": "error", "re": id, "code": error.code, "message": error.message])
        }
        let lines = answering(play.execute, id)
        send(play.executeStops ? Array(lines.prefix(lines.count / 2)) : play.executeLines.map { Array(lines.prefix($0)) } ?? lines)
        // The shell ticks once a run has ended, and hands the page that tick.
        if !play.executeStops, play.executeLines == nil, let tick = play.tickAfterRun {
            send([answering(tick, "shell-2")[0]])
        }
    }

    private func answer(cancel id: String, _ body: [String: Any]) {
        if body.str("target") == scanRequest, let done = scanHeld.last {
            // The scan ends at once and says it was cut short; only then does the cancel get its own done.
            expect(done.contains("\"canceled\":false"), "the recorded scan has no done line to cancel")
            scanHeld = []
            send([done.replacingOccurrences(of: "\"canceled\":false", with: "\"canceled\":true"), json(["event": "done", "re": id])])
        } else if body.str("target") != executeRequest {
            send(["event": "done", "re": id])
        }
    }
}

// MARK: - The scenarios

@MainActor
struct Scenario {
    let name: String
    var dark = false
    var play = Play()
    let run: @MainActor (Run) async -> Void
}

/// The results screen, as the engine's default selection leaves it.
@MainActor func results(_ r: Run) async {
    r.same(await r.screen(), "findings", "the screen")
    r.same(await r.phase(), "done", "the scan's phase")
}

/// What DESIGN.md fixes in numbers, read back from the drawn page: `[selector, property]` against a value.
/// A property starting with `#` is the element's own box: its height, its width, or how far its right edge
/// is from the window's.
@MainActor func drawn(_ r: Run, _ wants: [(String, String, String)]) async {
    for (selector, property, want) in wants {
        let got = await r.js("""
            const el = document.querySelector(selector);
            if (!el) return 'no such element';
            if (property === '#height') return String(el.getBoundingClientRect().height);
            if (property === '#width') return String(el.getBoundingClientRect().width);
            if (property === '#right') return String(innerWidth - el.getBoundingClientRect().right);
            const style = getComputedStyle(el);
            return property === 'font-family' ? style.fontFamily.split(',')[0] : style.getPropertyValue(property);
            """, ["selector": selector, "property": property]) as? String ?? ""
        r.same(got, want, "\(selector) \(property)")
    }
}

/// Ticks every row, so the plan is the transcript's plan of everything.
@MainActor func tickAll(_ r: Run) async {
    await r.js("""
        for (let i = 0; i < 99; i++) {
          const box = [...document.querySelectorAll('input[data-tick]:not([data-item])')].find((b) => !b.checked && !b.disabled);
          if (!box) break;
          box.click();
        }
        return null;
        """)
    await r.settle()
}

@MainActor func toPlan(_ r: Run) async {
    await tickAll(r)
    r.expect(await r.click("[data-do=review]"), "there is no Review plan to click")
    r.same(await r.screen(), "plan", "the screen after Review plan")
}

/// To the confirm step by a real Return on "Run plan…": that Return works on an ordinary button is what makes
/// its doing nothing on the confirm step mean something.
@MainActor func toConfirm(_ r: Run) async {
    await toPlan(r)
    await r.focus("[data-do=confirm]")
    await r.pressReturn()
    r.same(await r.screen(), "confirm", "the screen after Return on Run plan…")
}

@MainActor func toRun(_ r: Run) async {
    await toConfirm(r)
    await r.click("#read")
    await r.click("#go")
}

/// The engine exits after the page was sent the first `lines` journal lines of `section`, a run of the plan of
/// everything: the note counts the done lines it was sent, offers undo, and names no item.
@MainActor func stoppedNote(_ r: Run, _ section: String, _ lines: Int) async {
    await toRun(r)
    let done = r.transcript.lines(section).prefix(lines).map(parse).filter { $0.str("event") == "journal" && $0.obj("entry").str("event") == "done" }.count
    r.send(["event": "shell", "state": "engine_down", "message": "the engine exited with status 2"])
    await r.settle()
    let me = r.transcript.event("hello", "hello").str("self")
    r.same(await r.text(".note"), "The engine stopped during the run, after reporting \(done) of 25 actions done. If it moved anything, \(me) undo \(r.planID) puts it back; an item it was moving as it stopped may be in the Trash or in regrow staging, and undo does not know of it.", "the note")
    await r.shoot()
}

/// The page's rows and tick boxes against the scan's findings, with "Safe to clear" opened so each is a row.
@MainActor func checkRows(_ r: Run, _ scan: String) async {
    let t = r.transcript
    if await r.count("[data-do=fold]") == 1, await r.count(".fold") == 1 { await r.click("[data-do=fold]") }
    let rows = t.rows(scan).map { $0.obj("finding").obj("rule").str("id") }
    let selectable = Set(t.events(scan, "finding").filter { !$0.strs("select").isEmpty }.map { $0.obj("finding").obj("rule").str("id") })
    let drawn = await r.js("return [...document.querySelectorAll('.entry')].map((el) => el.dataset.rule)") as? [String] ?? []
    r.same(drawn.sorted(), rows.sorted(), "the rows drawn")
    let boxed = await r.js("return [...document.querySelectorAll('.entry')].filter((el) => el.querySelector(':scope > .chk input')).map((el) => el.dataset.rule)") as? [String] ?? []
    r.same(Set(boxed), selectable.intersection(rows), "the rows with a tick box")
    r.same(await r.count("input[data-tick]:not([data-item])"), selectable.intersection(rows).count, "the number of tick boxes")
}

@MainActor let scenarios: [Scenario] = {
    let lit: [Scenario] = [
        Scenario(name: "scanning", play: Play(scanStops: true)) { r in
            let t = r.transcript
            let all = t.events("scan", "finding"), half = Array(all.prefix(all.count / 2))
            let drawn = half.filter { !$0.obj("finding").objs("items").isEmpty || !$0.obj("finding").str("error").isEmpty }
            r.same(await r.phase(), "scanning", "the scan's phase")
            r.has(await r.text(".top .status"), "\(half.count) of \(all.count) rules have reported", "the status")
            r.same(await r.texts(".four b"), Array(repeating: "pending", count: 4), "the four totals")
            r.same(await r.count(".fold [data-rule], .entry"), drawn.count, "the rows drawn so far")
            r.same(await r.count(".fresh"), 2, "the rows highlighted as new")
            r.same(await r.count("input[data-tick]:not(:disabled)"), 0, "tick boxes that can be changed while scanning")
            r.same(await r.count("input[data-tick]:checked"), 0, "tick boxes ticked before the engine said which")
            r.expect(await r.flag("return document.querySelector('.tray .btn').disabled"), "Review plan is open during a scan")
            r.same(r.sent("plan").count, 0, "plan requests during a scan")
            await r.shoot()
        },
        Scenario(name: "results") { r in
            let t = r.transcript
            await results(r)
            let summary = t.event("scan", "summary").obj("totals"), plan = t.event("plan-default", "plan")
            let buckets = ["frees_now", "after_trash", "exported", "shown_only", "macos_managed"]
            r.expect(summary.int("exported") > 0, "the fixture scan holds a volume")
            r.same(await r.texts(".four b"), buckets.map { human(summary.int($0)) }, "the five totals")
            r.has(await r.text(".apart"), "Five totals, never added up", "the note under the totals")
            r.same(await r.count(".four .t"), 5, "the totals drawn")
            r.same(await r.js("return getComputedStyle(document.querySelector('.four')).gridTemplateColumns.split(' ').length") as? Int ?? 0, 5, "the columns of the totals")
            let volumeRow = await r.text(".entry[data-rule=docker-volumes-named]")
            r.has(volumeRow, "after a tarball goes to regrow staging, no undo", "what the volume row says it runs")
            r.has(volumeRow, "in a tarball until you delete it", "when the volume row's space comes back")
            let days = t.event("tick", "tick")["days_to_full"] as? Double ?? 0
            r.has(await r.text(".lead h2"), "full in about \(Int(days.rounded())) day", "the headline")
            // The engine's own alert prints the same free space: the page's figure must read as it does.
            let headroom = t.event("scan", "headroom")
            r.has(t.event("tick", "tick").objs("alerts").map { $0.str("message") }.joined(separator: " "), human(headroom.int("free")) + " left", "the transcript's alert")
            r.has(await r.text(".figures"), human(headroom.int("free")) + " free of " + human(headroom.int("total")), "the free figure")
            r.has(await r.text(".figures"), human(headroom.int("swap_used")) + " swap in use", "the swap figure")
            // One plan request with no select; its answer says what starts ticked and fills the tray.
            r.same(r.sent("plan").count, 1, "plan requests after the scan")
            r.expect(r.sent("plan")[0]["select"] == nil, "the first plan request carries a select")
            let ticked = t.ticked(by: "plan-default", in: "scan")
            let rules = t.events("scan", "finding").filter { !ticked.isDisjoint(with: $0.strs("select")) }.count
            let tray = await r.text(".tray .say")
            r.has(tray, "\(rules) findings ticked", "the tray")
            r.has(tray, human(plan.obj("totals").int("frees_now")) + " frees now", "the tray")
            r.has(tray, human(plan.obj("totals").int("after_trash")) + " after the Trash is emptied", "the tray")
            r.expect(plan.obj("totals").int("exported") == 0 && !tray.contains("volume exports"), "the tray names volume exports when the default plan has none")
            r.has(tray, "\(plan.obj("plan").objs("skipped").count) skipped", "the tray")
            let named = await r.count(".fold [data-rule]"), entries = await r.count(".entry")
            let more = Int(await r.js("return (document.querySelector('.fold .rest') || {dataset: {}}).dataset.more || '0'") as? String ?? "") ?? 0
            r.same(named + more + entries, t.rows("scan").count, "rows, the folded ones counted")
            r.same(named, 6, "names in the folded line")
            r.same(await r.count(".sec h3"), 3, "sections")
            r.same(await r.texts(".sec h3"), ["Safe to clear", "Your call", "Only shown"], "the order of sections")
            r.expect(await r.flag("return getComputedStyle(document.body).webkitUserSelect === 'none'"), "the page's text can be selected")
            let paper = await r.paper()
            await r.shoot()

            if windowSize.width == 1096 {
                await drawn(r, [
                    (".top", "#height", "42"), (".tray", "#height", "60"), (".page", "padding-left", "32px"), (".entry", "#right", "32"), (".top button", "#right", "32"), (".tray .btn", "#right", "32"),
                    (".top h1", "font-family", "ui-serif"), (".top h1", "font-size", "17px"), (".top h1", "font-weight", "600"),
                    (".lead h2", "font-family", "ui-serif"), (".lead h2", "font-size", "23px"), (".lead h2", "font-weight", "600"),
                    (".sec h3", "font-family", "ui-serif"), (".sec h3", "font-size", "17px"), (".sec h3", "font-weight", "600"),
                    (".entry h4", "font-family", "ui-serif"), (".entry h4", "font-size", "16px"), (".entry h4", "font-weight", "600"),
                    (".entry .story", "font-family", "ui-serif"), (".entry .story", "font-size", "14px"), (".entry .story", "font-weight", "400"),
                    ("body", "font-family", "system-ui"), ("body", "font-size", "13px"),
                    (".entry .cost", "font-size", "12.5px"), (".apart", "font-size", "12px"),
                    (".four b", "font-size", "20px"), (".four b", "font-weight", "650"), (".four b", "font-variant-numeric", "tabular-nums"),
                    (".side b", "font-size", "15px"), (".side b", "font-weight", "650"), (".side b", "font-variant-numeric", "tabular-nums"),
                    (".btn", "font-size", "13.5px"), (".btn", "font-weight", "650"), (".btn", "border-top-left-radius", "8px"),
                    (".entry", "grid-template-columns", "28px 562px 246px"), (".side", "grid-template-columns", "148px 88px"), (".side", "column-gap", "10px"),
                    (".bar", "#height", "6"), (".four .rule", "#height", "4"), (".meter", "#height", "6"), (".meter", "column-gap", "2px"),
                    (".entry", "border-bottom-width", "1px"), (".sec > header", "border-bottom-width", "1px"), (".tray", "border-top-width", "1px"),
                    (".entry .cost .mono", "font-size", "12px"), (".entry .cost .mono", "font-family", "ui-monospace"),
                ])
                // All bars share one scale: a full bar is the largest row.
                let widths = await r.js("return [...document.querySelectorAll('.entry .bar i')].map((el) => el.getBoundingClientRect().width / el.parentElement.getBoundingClientRect().width)") as? [Double] ?? []
                let exclusive = t.event("scan", "summary").obj("exclusive")
                let own = { (rule: String) -> Double in Double(exclusive.filter { $0.key.hasPrefix(rule + "/") }.reduce(Int64(0)) { $0 + exclusive.int($1.key) }) }
                let largest = t.rows("scan").map { own($0.obj("finding").obj("rule").str("id")) }.max() ?? 1
                r.expect(abs((widths.first ?? 0) - own("node-modules-dirs") / largest) < 0.002, "the first bar is not drawn on the scale of the largest finding: \(widths.first ?? 0)")
            }

            // data-theme on <html> overrides the system theme.
            await r.js("document.documentElement.dataset.theme = paper.startsWith('rgb(2') ? 'dark' : 'light'; return null", ["paper": paper])
            let other = await r.paper()
            r.expect(other != paper, "data-theme does not override the system theme")
            await r.js("delete document.documentElement.dataset.theme; return null")
            r.same(await r.paper(), paper, "the theme without data-theme")

            await checkRows(r, "scan")
            r.same(await r.count("input[data-tick]:checked"), t.events("scan", "finding").filter { !$0.strs("select").isEmpty && ticked.isSuperset(of: $0.strs("select")) }.count, "rows that start ticked")
        },
        Scenario(name: "results-open", play: Play(leaveOpen: true)) { r in
            let t = r.transcript
            await tickAll(r)
            r.same(r.sent("plan").last?.strs("select").count ?? 0, t.atoms("scan").count, "atoms sent once every row is ticked")
            await r.click("[data-do=fold]")
            let rule = "node-modules-dirs"
            let finding = t.events("scan", "finding").first { $0.obj("finding").obj("rule").str("id") == rule } ?? [:]
            let items = finding.obj("finding").objs("items"), select = finding.strs("select")
            await r.click(".entry[data-rule=\(rule)] [data-do=open]")
            await r.click(".entry[data-rule=brew-cache] [data-do=open]")
            await checkRows(r, "scan")
            let row = ".entry[data-rule=\(rule)]"
            r.same(await r.count("\(row) .item"), items.count, "item rows")
            r.same(await r.count("\(row) .item input"), select.count, "item tick boxes")
            let boxed = await r.js("return [...document.querySelectorAll(row + ' .item')].filter((el) => el.querySelector('input')).map((el) => el.dataset.id)", ["row": row]) as? [String] ?? []
            r.same(Set(boxed), Set(select), "the items with a tick box")
            r.has(await r.text("\(row) [data-do=open]"), "\(select.count) of \(items.count) folders", "the count beside the title")
            r.same(await r.count("\(row) .item .nosize"), items.filter { ($0["partial"] as? Bool ?? false) && $0.int("bytes") == 0 }.count, "items that read unreadable")
            r.has(await r.text("\(row) .item"), "~/", "an item's path")
            r.expect(!(await r.text("\(row) .items")).contains(NSHomeDirectory()), "an item's path is not written under ~")
            // A row ticked as a whole opens to its items without tick boxes.
            r.same(await r.count(".entry[data-rule=brew-cache] .item"), 2, "the items of a whole-rule row")
            r.same(await r.count(".entry[data-rule=brew-cache] .item input"), 0, "tick boxes on the items of a whole-rule row")
            await r.shoot()
            await r.shoot("-items", top: row)

            // One item unticked: the row is ticked in part, and what the page sends is still only atoms it was given.
            await r.click("\(row) .item input")
            r.has(await r.text("\(row) [data-do=open]"), "\(select.count - 1) of \(items.count) folders", "the count after one is unticked")
            r.expect(await r.flag("return document.querySelector('\(row) > .chk input').indeterminate"), "a row ticked in part is not drawn so")
            r.same(r.sent("plan").last?.strs("select").count ?? 0, t.atoms("scan").count - 1, "atoms sent after one is unticked")
            r.has(await r.text(".tray .say"), "counting", "the tray while the engine counts")
        },
        Scenario(name: "copy") { r in
            let t = r.transcript
            await results(r)
            // The page's words can be selected; its controls and the tray cannot.
            await drawn(r, [
                (".lead h2", "-webkit-user-select", "text"), (".entry .story", "-webkit-user-select", "text"),
                (".entry .cost", "-webkit-user-select", "text"), (".page button", "-webkit-user-select", "none"),
                (".tray .say", "-webkit-user-select", "none"),
            ])
            // A selection outlasts an event that changes nothing on the page.
            let story = await r.js("const el = document.querySelector('.entry .story'); getSelection().selectAllChildren(el); return getSelection().toString()") as? String ?? ""
            r.expect(!story.isEmpty, "a story could not be selected")
            r.send([t.lines("hello")[0]])
            await r.settle()
            r.same(await r.js("return getSelection().toString()") as? String ?? "", story, "the selection after an event that changed nothing on the page")
            // A finding's command, whole, where it runs as drawn.
            let runs = t.events("scan", "finding").first { e in
                let rule = e.obj("finding").obj("rule"), command = rule.strs("native_command")
                return rule.str("risk") == "caution" && e.str("bucket") == "frees_now" && !command.isEmpty && !command.joined().contains("{")
            }
            let rule = runs?.obj("finding").obj("rule") ?? [:]
            r.expect(runs != nil, "the scan holds no caution finding whose command runs as drawn")
            await r.click("[data-rule=\(rule.str("id"))] .cost .copy")
            r.same(r.pasted.last ?? "", shellLine(rule.strs("native_command")), "what Copy beside a finding's command put on the pasteboard")
            r.same(await r.js("return document.querySelector('[data-rule=\(rule.str("id"))] .cost .copy').textContent") as? String ?? "", "Copied", "a command's Copy once clicked")
            // A surface-only rule's how, quoted in its note after a colon: a line of its own, which ends the
            // sentence without the stop the note writes after it.
            await drawn(r, [("[data-rule=apfs-purgeable] .cost .cmd", "display", "block")])
            r.same(await r.text("[data-rule=apfs-purgeable] .cost .cmd"), "tmutil listlocalsnapshots /", "a shown-only rule's command on its own line")
            r.has(await r.text("[data-rule=apfs-purgeable] .cost") + "|", "most of it: tmutil listlocalsnapshots /|", "a shown-only rule's note, its stop after the command not drawn")
            await r.click("[data-rule=apfs-purgeable] .cost .copy")
            r.same(r.pasted.last ?? "", "tmutil listlocalsnapshots /", "what Copy beside a shown-only rule's command put on the pasteboard")
            await r.shoot("-command", top: "[data-rule=apfs-purgeable]")
            // An item's path, whole, where it is drawn with ~.
            let backups = t.events("scan", "finding").first { $0.obj("finding").obj("rule").str("id") == "ios-backups" }?.obj("finding").objs("items") ?? []
            let largest = backups.max { $0.int("bytes") < $1.int("bytes") }?.str("path") ?? ""
            await r.click("[data-rule=ios-backups] [data-do=open]")
            r.has(await r.text("[data-rule=ios-backups] .item .mono"), "~/Library", "an item's path, drawn")
            await r.click("[data-rule=ios-backups] .item .copy")
            r.same(r.pasted.last ?? "", largest, "what Copy beside an item's path put on the pasteboard")
            // A Copy pressed from the keyboard keeps the focus once its region is drawn again.
            let held = await r.js("""
                const all = [...document.querySelectorAll('.page .copy')], el = all[all.length - 1];
                el.focus(); el.click(); return el.dataset.text
                """) as? String ?? ""
            await r.settle()
            r.same(await r.js("return (document.activeElement && document.activeElement.dataset.text) || ''") as? String ?? "", held, "the focus after a Copy pressed from the keyboard")
            // The home folder is drawn as ~ only where it starts a word: after = or : a shell keeps ~ as written.
            let homeDir = backups.first.map { String($0.str("path").dropLast($0.str("key").count - 1)) } ?? ""
            r.expect(homeDir.hasPrefix("/"), "the transcript's backups name no home folder")
            let drawn = await r.js("return drawnLine([h + '/x', '--prefix=' + h + '/opt', '/usr/bin:' + h + '/bin', h])", ["h": homeDir]) as? String ?? ""
            r.same(drawn, "~/x --prefix=\(homeDir)/opt /usr/bin:\(homeDir)/bin ~", "a command drawn with the home folder after = and :")
            // A rule's command that an item fills in has no Copy. It is drawn as a shell reads it, its {arg} or
            // {path} word as written.
            r.same(await r.text(".entry[data-rule=docker-volumes-named] .cost .mono"), "docker volume rm {arg}", "a rule's command an item fills in, drawn")
            r.same(await r.count(".entry[data-rule=docker-volumes-named] .cost .copy"), 0, "Copy links beside a rule's command an item fills in")
            let template = ["/bin/sh", "-c", #""$1" runtime delete "$2" || exit; n=$((n+1)); out=$("$1" list)"#, "sh", "/x/simctl", "{arg}"]
            let ruled = shellLine(Array(template.dropLast())) + " {arg}"
            r.same(await r.js("return ruleLine(argv)", ["argv": template]) as? String ?? "", ruled, "a rule's command an item fills in, its words quoted for a shell")
            let volumes = "S.scan.rows.find((row) => row.id === 'docker-volumes-named').rule"
            let recorded = await r.js("return \(volumes).native_command") as? [String] ?? []
            r.same(recorded, ["docker", "volume", "rm", "{arg}"], "the volumes rule's recorded command")
            _ = await r.js("\(volumes).native_command = argv", ["argv": template])
            r.send([t.lines("hello")[0]])
            await r.settle()
            r.same(await r.text(".entry[data-rule=docker-volumes-named] .cost .mono"), ruled, "a row's command an item fills in, drawn")
            _ = await r.js("\(volumes).native_command = argv", ["argv": recorded])
            r.send([t.lines("hello")[0]])
            await r.settle()
            // regrow quoted in a rule's text is started as the engine names itself.
            let named = await r.js("""
                const d = document.createElement('div'); d.innerHTML = sentence('To see how to stop them: `regrow doctor`.').s;
                return [d.querySelector('.copy').dataset.text, d.querySelector('.mono').textContent, cli()]
                """) as? [String] ?? []
            r.expect(named.count == 3 && named[2] != "regrow", "the engine names itself by its path in this transcript: \(named)")
            r.same(named.first ?? "", (named.last ?? "") + " doctor", "what Copy beside a rule's regrow command puts on the pasteboard")
            r.same(named.count == 3 ? named[1] : "", (named.last ?? "") + " doctor", "a rule's regrow command, drawn")
            r.same(r.sentShell("copy"), r.pasted.count, "copies the shell was sent")
            await r.shoot(top: "[data-rule=\(rule.str("id"))]")
        },
        Scenario(name: "errors", play: Play(scan: "scan-errors")) { r in
            let t = r.transcript
            await results(r)
            let failed = t.events("scan-errors", "finding").map { $0.obj("finding") }.filter { !$0.str("error").isEmpty }
            r.same(await r.texts(".sec h3").first ?? "", "Not scanned", "the first section")
            r.same(await r.texts(".sec.bad .entry .err"), failed.map { $0.str("error") }, "the errors shown, in the order they came")
            r.has(await r.text(".top .status"), "\(failed.count) ended with an error", "the status")
            r.same(await r.count(".sec.bad input"), 0, "tick boxes on rules that failed")
            r.same(await r.count(".sec.bad .bar"), 0, "bars on rules that failed")
            let totals = t.event("scan-errors", "summary").obj("totals")
            r.same(await r.texts(".four b").first ?? "", human(totals.int("frees_now")), "the first total")
            await r.shoot()
            await checkRows(r, "scan-errors")
        },
        Scenario(name: "noforecast-short", play: Play(tick: "tick-short")) { r in
            let headroom = r.transcript.event("scan", "headroom")
            r.same(await r.text(".lead h2"), "\(human(headroom.int("free"))) free of \(human(headroom.int("total"))).", "the headline")
            r.has(await r.text(".lead .aside"), "three samples over three hours", "why there is no forecast")
            r.has(await r.text(".figures"), human(headroom.int("total") - headroom.int("free") - headroom.int("purgeable")) + " taken", "the first figure")
            await r.shoot()
        },
        Scenario(name: "noforecast-steady", play: Play(tick: "tick-steady")) { r in
            r.has(await r.text(".lead h2"), " free of ", "the headline")
            r.has(await r.text(".lead .aside"), "steady or rising", "why there is no forecast")
            await r.shoot()
        },
        Scenario(name: "fda-denied", play: Play(fdaDenied: true)) { r in
            let partial = r.transcript.events("scan", "finding").filter { $0.obj("finding").objs("items").contains { $0["partial"] as? Bool ?? false } }.count
            r.has(await r.text(".note"), "Full Disk Access is off, so \(partial) findings were read only in part", "the notice")
            r.same(await r.text(".note button"), "Open Full Disk Access settings", "the notice's action")
            await r.shoot()
            await r.click(".note button")
            r.same(r.sentShell("open-full-disk-access"), 1, "open-full-disk-access messages")
            r.same(await r.text(".note button"), "Relaunch regrow to apply", "the notice's action once the settings were opened")
            await r.click(".note button")
            r.same(r.sentShell("relaunch"), 1, "relaunch messages")
            await r.click(".entry[data-rule=node-modules-dirs] [data-do=open]")
            r.has(await r.text(".entry[data-rule=node-modules-dirs] .items"), "needs Full Disk Access", "what an unreadable item needs")
        },
        Scenario(name: "general") { r in
            await results(r)
            await r.js("document.getElementById('page').scrollTop = 300; return null")
            let top = await r.js("return document.getElementById('page').scrollTop") as? Double ?? 0
            r.expect(top > 0, "the scan scrolls")
            r.same(await r.js("return document.getElementById('nav-scan').getAttribute('aria-current')") as? String ?? "", "page", "the sidebar's current section on the scan")
            await r.click("#nav-general")
            r.same(await r.js("return document.documentElement.dataset.pane") as? String ?? "", "general", "the section shown")
            r.same(await r.js("return document.getElementById('nav-general').getAttribute('aria-current')") as? String ?? "", "page", "the sidebar's current section")
            r.same(await r.text(".top h1"), "General", "the top bar's title")
            r.same(await r.count(".top button"), 0, "actions in General's top bar")
            r.same(await r.texts("#pane .sec h3"), ["Startup", "Permissions", "About"], "General's sections")
            r.same(await r.js("return document.querySelector('[data-setting=login] .switch').getAttribute('aria-checked')") as? String ?? "", "true", "Start with Mac's switch")
            r.same(await r.text("[data-setting=fda] .ctl"), "on", "Full Disk Access granted")
            r.same(await r.text("[data-setting=finder] .ctl"), "not asked yet", "Control of Finder before macOS asked")
            r.same(await r.text("[data-setting=notifications] .state"), "on", "Notifications allowed")
            r.same(await r.text("[data-setting=notifications] button"), "Open settings", "Notifications' action")
            r.same(await r.text("#pane .sec header span"), "", "the count of grants off when none is")
            r.same(await r.text("[data-setting=version] .ctl"), "regrow 0.5.0", "the version")
            r.same(await r.text("[data-setting=engine] .mono"), "~/Applications/Regrow.app/Contents/MacOS/regrow-engine", "the engine's path")
            await r.click("[data-setting=engine] .copy")
            r.same(r.pasted.last ?? "", "/Users/dev/Applications/Regrow.app/Contents/MacOS/regrow-engine", "what Copy beside the engine's path put on the pasteboard")
            r.same(await r.text("[data-setting=log] .mono"), "~/Library/Logs/regrow/app.log", "the log, from the home the scan taught the page")
            await drawn(r, [
                (".nav", "#width", "196"), (".nav .name", "font-family", "ui-serif"), (".nav .name", "font-size", "17px"),
                ("#pane", "padding-left", "32px"), (".set h4", "font-size", "13.5px"), (".set h4", "font-weight", "650"),
                (".switch", "#height", "19"), ("[data-setting=login] .switch", "#right", "32"),
            ])
            await r.shoot()

            await r.click("[data-setting=login] .switch")
            r.same(r.sentShell("login-item-off"), 1, "login-item-off messages")
            r.same(await r.js("return document.querySelector('[data-setting=login] .switch').getAttribute('aria-checked')") as? String ?? "", "true", "the switch before the shell says it moved")
            var off = Play.installed
            off["login_item"] = "off"
            r.send(off)
            await r.settle()
            r.same(await r.js("return document.querySelector('[data-setting=login] .switch').getAttribute('aria-checked')") as? String ?? "", "false", "the switch once the shell says it moved")
            await r.click("[data-setting=login] .switch")
            r.same(r.sentShell("login-item-on"), 1, "login-item-on messages")
            await r.click("[data-setting=log] button")
            r.same(r.sentShell("show-log"), 1, "show-log messages")
            await r.click("[data-setting=staging] button")
            r.same(r.sentShell("show-staging"), 1, "show-staging messages")
            await r.click("[data-setting=notifications] button")
            r.same(r.sentShell("notification-settings"), 1, "notification-settings messages")

            await r.click("#nav-scan")
            r.same(await r.text(".top h1"), "Scan", "the top bar's title on the scan")
            r.same(await r.js("return document.getElementById('page').scrollTop") as? Double ?? 0, top, "where the scan was scrolled to, kept while General showed")
            r.same(await r.screen(), "findings", "the scan's screen, kept while General showed")
        },
        Scenario(name: "general-off", play: Play(fdaDenied: true, settings: [
            "event": "shell", "state": "settings", "login_item": "needs_approval", "notifications": "denied", "finder": "denied",
            "log": "/Users/dev/Library/Logs/regrow/app.log", "staging": "/Users/dev/.local/state/regrow/staging",
        ])) { r in
            await results(r)
            r.same(await r.text("#nav-general .fig"), "3", "the grants off, beside General in the sidebar")
            await r.click("#nav-general")
            r.same(await r.text("#pane .sec:nth-of-type(2) header span"), "3 of 3 off", "the count of grants off")
            let partial = r.transcript.events("scan", "finding").filter { $0.obj("finding").objs("items").contains { $0["partial"] as? Bool ?? false } }.count
            r.has(await r.text("[data-setting=fda] p"), "Off: \(partial) findings of the last scan were read only in part", "what Full Disk Access off costs")
            r.same(await r.text("[data-setting=fda] button"), "Open settings", "Full Disk Access's action")
            r.same(await r.text("[data-setting=login] .state"), "needs approval", "Start with Mac awaiting approval")
            r.same(await r.text("[data-setting=login] button"), "Open Login Items settings", "Start with Mac's action awaiting approval")
            r.same(await r.count("[data-setting=login] .switch"), 0, "a switch while macOS holds the login item")
            await r.shoot()
            await r.click("[data-setting=fda] button")
            r.same(r.sentShell("open-full-disk-access"), 1, "open-full-disk-access messages")
            r.same(await r.text("[data-setting=fda] button"), "Relaunch to apply", "Full Disk Access's action once its settings were opened")
            await r.click("[data-setting=fda] button")
            r.same(r.sentShell("relaunch"), 1, "relaunch messages")
            await r.click("[data-setting=login] button")
            r.same(r.sentShell("login-item-settings"), 1, "login-item-settings messages")
            await r.click("[data-setting=finder] button")
            r.same(r.sentShell("automation-settings"), 1, "automation-settings messages")
            await r.click("#nav-scan")
            r.same(await r.text(".note button"), "Relaunch regrow to apply", "the scan's notice, once General opened the settings")
        },
        Scenario(name: "general-copy", play: Play(opened: true, settings: [
            "event": "shell", "state": "settings", "notifications": "unknown", "finder": "not_asked", "log": "/tmp/regrow/app.log",
        ])) { r in
            await r.click("#nav-general")
            r.same(await r.text("[data-setting=login] p"), "Only the copy in ~/Applications can start with your Mac.", "Start with Mac in a copy elsewhere")
            r.same(await r.count("#pane .switch"), 0, "switches in a copy elsewhere")
            r.same(await r.text("[data-setting=notifications] .ctl"), "not available in this copy", "Notifications without a bundle")
            r.same(await r.count("[data-setting=staging]"), 0, "the staging row before the engine named its folder")
            r.same(await r.text("[data-setting=log] .mono"), "/tmp/regrow/app.log", "a log outside the home")
            await r.shoot()
        },
        Scenario(name: "general-latest-word") { r in
            await results(r)
            // The confirm step heard no; then the owner allowed it in System Settings, and the shell read that.
            r.send(["event": "shell", "state": "automation", "allowed": false])
            var allowed = Play.installed
            allowed["finder"] = "allowed"
            r.send(allowed)
            await r.settle()
            await r.click("#nav-general")
            r.same(await r.text("[data-setting=finder] .state"), "on", "Control of Finder, by the shell's later read")
            r.same(await r.count("[data-setting=finder] button"), 0, "Control of Finder's link while it is on")
            r.same(await r.text("#nav-general .fig"), "", "grants off beside General")
            r.send(["event": "shell", "state": "automation", "allowed": false])
            await r.settle()
            r.same(await r.text("[data-setting=finder] .state"), "off", "Control of Finder, by the confirm step's later answer")
            var unknown = Play.installed
            unknown["finder"] = "unknown"
            r.send(unknown)
            await r.settle()
            r.same(await r.text("[data-setting=finder] .state"), "unknown", "Control of Finder macOS did not report")
            r.has(await r.text("[data-setting=finder] p"), "macOS did not say", "why it is unknown")
            r.same(await r.text("#nav-general .fig"), "", "a grant macOS did not report, counted as off")
            r.same(await r.text("[data-setting=staging] .state"), human(r.transcript.event("tick", "tick").int("staged_bytes")), "staging's size, from the tick")
            r.same(await r.count("[data-setting=staging] button"), 1, "Show in Finder for staging with something there")
            await r.shoot()
        },
        Scenario(name: "general-scan-asked") { r in
            await results(r)
            await r.click("#nav-general")
            r.send(["event": "shell", "state": "scan"])
            await r.settle()
            r.same(await r.js("return document.documentElement.dataset.pane") as? String ?? "", "scan", "the section the menu's Scan shows")
            r.same(r.sent("scan").count, 2, "scans, the menu's included")
            await r.shoot()
        },
        Scenario(name: "general-run-ended", play: Play(executeStops: true)) { r in
            await toRun(r)
            await r.js("document.getElementById('page').scrollTop = 400; return null")
            await r.click("#nav-general")
            let lines = r.answering("execute", r.executeRequest)
            r.send(Array(lines.suffix(from: lines.count / 2)))
            await r.settle()
            r.same(await r.text("#nav-scan .fig"), "", "Scan's figure once the run has ended")
            await r.click("#nav-scan")
            r.same(await r.screen(), "result", "the screen once the run has ended")
            r.same(await r.js("return document.getElementById('page').scrollTop") as? Double ?? -1, 0, "the result's scroll, on a screen new since the scan was left")
            await r.shoot()
        },
        Scenario(name: "general-unknown", play: Play(fdaWord: "unknown", tick: nil)) { r in
            await results(r)
            await r.click("#nav-general")
            r.same(await r.text("[data-setting=fda] .state"), "unknown", "Full Disk Access macOS did not report")
            r.same(await r.text("[data-setting=fda] button"), "Open settings", "Full Disk Access's action while unknown")
            r.same(await r.text("#nav-general .fig"), "", "grants off beside General")
            r.same(await r.text("#pane .sec:nth-of-type(2) header span"), "", "the count of grants off")
            r.same(await r.text("[data-setting=staging] .state"), "…", "staging with no tick yet")
            r.same(await r.count("[data-setting=staging] button"), 0, "Show in Finder for staging with nothing there")
            r.send(["event": "shell", "state": "engine_down", "message": "the engine exited with status 1"])
            await r.settle()
            r.same(await r.text("#nav-scan .fig"), "stopped", "Scan's figure while the engine is down")
            await r.shoot()
        },
        Scenario(name: "general-staged-after-run", play: Play(tick: "tick-steady", tickAfterRun: "tick")) { r in
            await toRun(r)
            await r.settle()
            r.same(await r.screen(), "result", "the screen")
            r.same(r.sentShell("tick"), 0, "ticks the page asked for: the shell ticks by itself")
            await r.click("#nav-general")
            r.same(await r.text("[data-setting=staging] .state"), human(r.transcript.event("tick", "tick").int("staged_bytes")), "staging's size, from the tick after the run")
            r.same(await r.count("[data-setting=staging] button"), 1, "Show in Finder for staging once the run put something there")
            await r.shoot()
        },
        Scenario(name: "general-rescan-scroll") { r in
            await results(r)
            await r.js("document.getElementById('page').scrollTop = 400; return null")
            await r.click("#nav-general")
            r.send(["event": "shell", "state": "engine_down", "message": "the engine exited with status 2"])
            await r.settle()
            r.send([r.transcript.lines("hello")[0]])
            await r.settle()
            r.same(r.sent("scan").count, 2, "scans, the one after the engine came back included")
            await r.click("#nav-scan")
            await r.settle()
            r.same(await r.phase(), "done", "the new scan's phase")
            r.same(await r.js("return document.getElementById('page').scrollTop") as? Double ?? -1, 0, "a new scan's scroll, though the screen is the same")
            await r.shoot()
        },
        Scenario(name: "stats") { r in
            await results(r)
            r.same(r.sent("history").count, 1, "history requests once the engine said hello")
            let history = r.transcript.event("history", "history"), runs = history.objs("runs")
            let freed = runs.reduce(Int64(0)) { $0 + $1.int("frees_now") }
            r.same(await r.text("#nav-stats .fig"), human(freed), "Stats' figure in the sidebar: what runs freed")
            await r.click("#nav-stats")
            r.same(r.sent("history").count, 2, "history requests once Stats was opened")
            r.same(await r.text(".top h1"), "Stats", "the top bar's title")
            r.has(await r.text("#pane .lead h2"), "regrow has freed \(human(freed)) on this Mac since", "the headline")
            r.has(await r.text("#pane .lead .say"), "In \(runs.count) runs, \(runs.filter { $0.int("failed") > 0 }.count) with an action that failed.", "the runs, and those with a failure")
            r.same(await r.js("return [...document.querySelectorAll('#pane .four .t')].map((t) => t.dataset.bucket)") as? [String] ?? [], ["frees_now", "after_trash", "exported"], "the totals")
            r.same(await r.text("#pane .four [data-bucket=after_trash] b"), human(runs.reduce(Int64(0)) { $0 + $1.int("after_trash") }), "moved to the Trash")
            r.has(await r.text("#pane .apart"), "went to regrow staging", "what went to staging")
            r.same(await r.count("#pane .chart .dot"), runs.count, "a dot for each run")
            r.same(await r.count("#pane .run"), runs.count, "runs")
            let rules = Set(runs.flatMap { $0.obj("rules").keys })
            r.same(await r.count("#pane .entry[data-rule]"), min(8, rules.count), "rules, at most eight")
            r.same(await r.text("#pane .entry[data-rule=go-build-cache] h4"), history.obj("titles").str("go-build-cache"), "a rule by its title")
            let largest = runs.flatMap { $0.obj("rules").map { ($0.key, ($0.value as? NSNumber)?.int64Value ?? 0) } }
                .reduce(into: [String: Int64]()) { $0[$1.0, default: 0] += $1.1 }.max { $0.value < $1.value }
            r.same(await r.js("return document.querySelector('#pane .entry[data-rule]').dataset.rule") as? String ?? "", largest?.key ?? "", "the first rule, the largest")
            r.same(await r.count("#pane .run .tag.bad"), runs.filter { $0.int("failed") > 0 }.count, "runs marked with their failures")
            await r.js("""
                const plot = document.querySelector('#pane .plot').getBoundingClientRect();
                document.dispatchEvent(new MouseEvent('mousemove', { clientX: plot.left + plot.width / 2, clientY: plot.top + 40 }));
                return null
                """)
            r.has(await r.text("#pane .tip"), "free", "the crosshair's tip")
            await r.shoot()
            await r.click("#nav-scan")
            r.same(await r.screen(), "findings", "the scan, kept while Stats showed")
        },
        Scenario(name: "stats-empty", play: Play(historyEvent: ["event": "history", "runs": [Any](), "samples": [Any](), "titles": [String: Any]()])) { r in
            await results(r)
            r.same(await r.text("#nav-stats .fig"), "", "Stats' figure with nothing run")
            await r.click("#nav-stats")
            r.same(await r.text("#pane .lead h2"), "Nothing has run on this Mac yet.", "the headline")
            r.same(await r.count("#pane .four, #pane .run, #pane .entry"), 0, "totals, runs and rules with nothing run")
            r.has(await r.text("#pane"), "Not enough samples yet", "the chart with no samples")
            await r.shoot()
        },
        Scenario(name: "stats-run-ends-while-asking", play: Play(executeStops: true)) { r in
            // Stats asks while the run's last lines are on their way: the answer it gets may predate the run's end.
            await toRun(r)
            r.play.holdHistory = true
            await r.click("#nav-stats")
            let asked = r.sent("history").count
            let lines = r.answering("execute", r.executeRequest)
            r.send(Array(lines.suffix(from: lines.count / 2)))
            await r.settle()
            r.same(r.sent("history").count, asked, "history requests while one is on its way")
            r.play.holdHistory = false
            for id in r.heldHistory { r.send(r.answering("history", id)) }
            await r.settle()
            r.same(r.sent("history").count, asked + 1, "history requests once the one on its way has ended: one more, for the run")
            await r.shoot()
        },
        Scenario(name: "stats-after-a-prune", play: Play(historyEvent: ["event": "history", "titles": [String: Any](), "samples": [
            ["at": "2026-10-28T10:00:00Z", "total": 1e12, "free": 6e10, "purgeable": 0, "swap_used": 0],
            ["at": "2026-10-29T10:00:00Z", "total": 1e12, "free": 5e10, "purgeable": 0, "swap_used": 0],
        ], "runs": [
            // After the last sample: the tick samples before it prunes, and the next sample may not be in yet.
            ["id": "20261029-160000-0123456789abcdef0123456789abcdef", "start": "2026-10-29T16:00:00Z", "done": 1, "failed": 0, "undone": 0, "restorable": 0,
             "prune": true, "frees_now": 1_073_741_824, "after_trash": 0, "staged": 0, "exported": 0, "rules": ["go-build-cache": 1_073_741_824]],
        ]])) { r in
            // A watch tick whose autotrim pruned is a run of its own: Stats, open, reads the history again.
            await results(r)
            await r.click("#nav-stats")
            let asked = r.sent("history").count
            var tick = r.transcript.event("tick", "tick")
            tick["re"] = "shell-tick-2"
            tick["pruned"] = ["rule_id": "go-build-cache", "run": "20261029-160000-0123456789abcdef0123456789abcdef", "files": 12, "bytes": 1_073_741_824]
            r.send(tick)
            await r.settle()
            r.same(r.sent("history").count, asked + 1, "history requests after a tick that pruned")
            r.same(await r.count("#pane .chart .dot"), 1, "a dot for the prune, which started after the last sample")
            r.same(await r.js("return Math.round(parseFloat(document.querySelector('#pane .chart .dot').style.left))") as? Int ?? 0, 100, "the prune's dot at the right edge, in percent")
            await r.shoot()
        },
        Scenario(name: "stats-one-moment", play: Play(historyEvent: ["event": "history", "runs": [Any](), "titles": [String: Any](), "samples": [
            ["at": "2026-10-29T09:00:00Z", "total": 1e12, "free": 6e10, "purgeable": 0, "swap_used": 0],
            ["at": "2026-10-29T09:00:00Z", "total": 1e12, "free": 6e10, "purgeable": 0, "swap_used": 0],
        ]])) { r in
            await results(r)
            await r.click("#nav-stats")
            r.same(await r.count("#pane .chart"), 0, "no chart for samples that all share one time")
            r.has(await r.text("#pane"), "Not enough samples yet", "what shows in its place")
            await r.shoot()
        },
        Scenario(name: "stats-down", play: Play(holdHello: true)) { r in
            r.send(["event": "shell", "state": "engine_down", "message": "the engine exited with status 2"])
            await r.settle()
            await r.click("#nav-stats")
            r.same(await r.text("#pane .lead h2"), "regrow's engine has stopped.", "Stats while the engine is down")
            r.same(await r.text("#pane .lead .err"), "the engine exited with status 2", "the engine's reason")
            await r.shoot()
        },
        Scenario(name: "stats-large-disk", play: Play(historyEvent: ["event": "history", "runs": [Any](), "titles": [String: Any](), "samples": [
            ["at": "2026-10-28T09:00:00Z", "total": 8e12, "free": 4.5e12, "purgeable": 0, "swap_used": 0],
            ["at": "2026-10-29T09:00:00Z", "total": 8e12, "free": 4.4e12, "purgeable": 0, "swap_used": 0],
        ]])) { r in
            await results(r)
            await r.click("#nav-stats")
            r.same(await r.js("return document.querySelector('#pane .chart path.line').getAttribute('d').includes('NaN')") as? Bool ?? true, false, "the free-space line past 4000 GiB")
            r.same(await r.text("#pane .chart .y"), "0 GiB", "the chart's lowest gridline")
            r.has(await r.text("#pane .chart"), "6000 GiB", "the gridline above the samples, at a step of 2000 GiB")
            await r.shoot()
        },
        Scenario(name: "stats-failed", play: Play(historyError: "oplog.jsonl: line 7: invalid character 'x' looking for beginning of value")) { r in
            await results(r)
            await r.click("#nav-stats")
            r.same(await r.text("#pane .lead h2"), "regrow's history could not be read.", "the headline")
            r.same(await r.text("#pane .lead .err"), "oplog.jsonl: line 7: invalid character 'x' looking for beginning of value", "the engine's reason")
            r.has(await r.text("#pane .lead .aside"), "history in Terminal reads the same journal.", "what else reads it, with no guess at the cause")
            r.same(await r.count(".note"), 0, "notices on the scan for a history that failed")
            await r.shoot()
        },
        Scenario(name: "env-failed", play: Play(environment: "the login shell gave no answer within 5 s")) { r in
            r.has(await r.text(".note"), "could not read your login shell's environment", "the notice")
            r.has(await r.text(".note"), "the login shell gave no answer within 5 s", "the notice's reason")
            r.same(await r.count(".note button"), 0, "actions on the environment notice")
            await r.shoot()
            // A string from outside reaches the page as text, whatever it holds. The transcript has no such
            // string, so the shell's reason carries one.
            let hostile = "</span><img src=x onerror=\"document.title='x'\"> & 'so' on"
            r.send(["event": "shell", "state": "environment", "captured": false, "reason": hostile])
            await r.settle()
            r.same(await r.js("return document.querySelector('.note .mono').textContent") as? String ?? "", hostile, "the reason as text")
            r.same(await r.count("img"), 0, "elements made from a string the page was sent")
        },
        Scenario(name: "plan") { r in
            let t = r.transcript
            await toPlan(r)
            let event = t.event("plan-all", "plan"), plan = event.obj("plan"), actions = plan.objs("actions"), totals = event.obj("totals")
            r.same(r.sent("plan").last?.strs("select").sorted() ?? [], t.atoms("scan").sorted(), "the atoms Review plan sent")
            r.has(await r.text(".lead h2"), "\(actions.count) actions, nothing has run yet", "the headline")
            r.same(await r.text(".top .status"), "", "the top bar's status on the plan")
            r.same(await r.count(".top button"), 0, "actions in the top bar on the plan")
            let lead = await r.text(".lead .say")
            let volumes = actions.filter { $0.str("kind") == "native" && $0.str("pre_action") == "docker-volume-export" }.count
            r.expect(volumes > 0 && totals.int("exported") > 0, "the plan holds volumes")
            r.has(lead, human(totals.int("frees_now")) + " comes back as \(actions.filter { $0.str("kind") != "trash" }.count - volumes) commands run", "the lead")
            r.has(lead, human(totals.int("exported")) + " is \(volumes) docker volumes removed after a tarball of each goes to regrow staging", "the lead")
            r.has(lead, "regrow undo does not restore a volume", "the lead")
            r.same(await r.js("return document.querySelector('.act[data-rule=docker-volumes-named] .bar i').className") as? String ?? "", "exported", "the colour of a volume's bar")
            r.has(lead, human(totals.int("after_trash")) + " goes to the Trash in \(actions.filter { $0.str("kind") == "trash" }.count) moves", "the lead")
            let headroom = t.event("scan", "headroom")
            r.has(await r.text(".lead .figures"), human(headroom.int("free") + totals.int("frees_now")) + " free once the commands end, up from " + human(headroom.int("free")), "the meter's figure")
            // Skips first, each with the engine's reason; then the actions in the engine's order.
            r.same(await r.texts(".sec h3"), ["Skipped", "In the order they run"], "the sections")
            r.same(await r.texts(".act.skip p"), plan.objs("skipped").map { $0.str("reason").replacingOccurrences(of: "`", with: "") }, "the reasons")
            var order: [String] = []
            for action in actions where order.last != action.str("rule_id") { order.append(action.str("rule_id")) }
            r.same(await r.js("return [...document.querySelectorAll('.act:not(.skip)')].map((el) => el.dataset.rule)") as? [String] ?? [], order, "the order of actions")
            let last = actions.reversed().prefix { $0.str("rule_id") == order.last }.count
            r.same(await r.texts(".act .seq").last ?? "", last > 1 ? "\(actions.count - last + 1)–\(actions.count)" : "\(actions.count)", "the last group's numbers")
            r.has(await r.text(".tray .say"), "in the next 10 minutes", "how long the plan can run")
            if windowSize.width == 1096 {
                await drawn(r, [
                    (".act:not(.skip)", "grid-template-columns", "44px 546px 246px"), (".act h4", "font-family", "ui-serif"), (".act h4", "font-size", "15px"),
                    (".act .ln", "font-family", "ui-monospace"), (".act .ln", "font-size", "12px"), (".act .ln", "-webkit-user-select", "text"),
                    (".lead .say", "font-family", "ui-serif"), (".act .side b", "font-size", "14px"),
                ])
            }
            r.expect(!(await r.text(".page")).contains(NSHomeDirectory()), "a path on the plan is not written under ~")
            await r.shoot()
            // A group opens to each of its commands, so each can be read before it is confirmed.
            let group = ".act[data-rule=node-modules-dirs]"
            let moves = actions.filter { $0.str("rule_id") == "node-modules-dirs" }
            r.has(await r.text("\(group) [data-do=open]"), "and \(moves.count - 1) more like it", "the folded group")
            await r.click("\(group) [data-do=open]")
            r.same(await r.count("\(group) .ln"), moves.count, "the lines of an opened group")
            r.same(await r.js("return document.querySelector('\(group) .ln [title]').title") as? String ?? "", shellLine(moves[0].strs("command")), "the command in the title, as a shell reads it")
            r.same(r.sent("execute").count, 0, "executes from the plan")
        },
        Scenario(name: "copy-plan") { r in
            await toPlan(r)
            let plan = r.transcript.event("plan-all", "plan").obj("plan"), first = plan.objs("actions")[0]
            let whole = first.str("kind") == "trash" && !first.str("path").isEmpty ? first.str("path") : shellLine(first.strs("command"))
            r.same(await r.count(".act:not(.skip) .ln .copy"), await r.count(".act:not(.skip) .ln"), "plan lines without a Copy")
            await r.click(".act:not(.skip) .ln .copy")
            r.same(r.pasted.last ?? "", whole, "what Copy beside the first action put on the pasteboard")
            // A skip's reason that names a command in Terminal: the command, without its backticks. A bare name
            // quoted in a reason (a program not on the PATH) has no Copy.
            let quoted = plan.objs("skipped").map { $0.str("reason").components(separatedBy: "`") }
                .flatMap { parts in parts.indices.filter { $0 % 2 == 1 && $0 < parts.count - 1 }.map { parts[$0] } }
            let commands = quoted.filter { $0.contains(" ") || $0.contains("/") }
            r.expect(!commands.isEmpty && commands.count < quoted.count, "the skips quote no command, or no bare name")
            r.same(await r.count(".act.skip p .copy"), commands.count, "Copy controls in the skips' reasons")
            // The command a skip leaves you to run in Terminal comes after a colon: a line of its own.
            let own = plan.objs("skipped").map { $0.str("reason").components(separatedBy: "`") }
                .flatMap { parts in
                    parts.indices.filter { i in
                        i % 2 == 1 && i < parts.count - 1 && parts[i].contains(" ")
                            && parts[i - 1].trimmingCharacters(in: .whitespaces).hasSuffix(":")
                    }
                }
            r.expect(!own.isEmpty, "no skip brings in its command with a colon")
            r.same(await r.count(".act.skip p .cmd"), own.count, "skips' commands on a line of their own")
            await r.click(".act.skip p .copy")
            r.same(r.pasted.last ?? "", commands.first ?? "", "what Copy beside a skip's command put on the pasteboard")
            r.same(r.sent("execute").count, 0, "executes from copying")
            await r.shoot()
        },
        Scenario(name: "plan-expired") { r in
            await toConfirm(r)
            await r.click("#read")
            r.expect(!(await r.flag("return document.getElementById('go').disabled")), "the run button is locked on a plan that can still run")
            // The plan's time runs out under the confirm step: the page goes back to the plan by itself.
            await r.jump(minutes: 11)
            await r.wait(for: "the page leaving the confirm step once the plan is too old", "document.documentElement.dataset.screen === 'plan'")
            r.same(await r.text(".tray .btn:not(.quiet)"), "Plan again", "the tray's action once the plan is too old")
            r.same(await r.count("[data-do=confirm], [data-do=run], #go"), 0, "ways to run a plan that is too old")
            r.has(await r.text(".tray .say"), "too old to run", "the tray")
            await r.shoot()
            await r.click("[data-do=again]")
            r.same(r.sent("execute").count, 0, "executes of a plan that is too old")
            r.same(await r.text(".tray .btn:not(.quiet)"), "Run plan…", "the tray's action on the fresh plan")
        },
        Scenario(name: "expired-click", play: Play(clockStops: true)) { r in
            // The page's timers are stopped, so only the click's own check can know the plan is too old.
            await toConfirm(r)
            await r.click("#read")
            await r.jump(minutes: 11)
            r.same(await r.screen(), "confirm", "the screen with the page's timers stopped")
            r.expect(!(await r.flag("return document.getElementById('go').disabled")), "the run button was redrawn with the page's timers stopped")
            await r.click("#go")
            r.same(r.sent("execute").count, 0, "executes of a plan that ran out before the click")
            r.same(await r.screen(), "plan", "the screen after a click on a plan that is too old")
            await r.shoot()
        },
        Scenario(name: "confirm", play: Play(automation: nil)) { r in
            await toConfirm(r)
            let actions = r.transcript.event("plan-all", "plan").obj("plan").objs("actions")
            r.same(r.sentShell("automation"), 1, "automation questions on entering the confirm step")
            r.has(await r.text(".ask dl"), "Asking macOS whether regrow may control Finder", "the Trash line while the answer is out")
            let exports = r.transcript.event("plan-all", "plan").obj("totals").int("exported"), volumes = actions.filter { $0.str("pre_action") == "docker-volume-export" }.count
            r.has(await r.text(".ask dl"), human(exports) + "\(volumes) docker volumes removed, with no undo.", "the volume line")
            r.has(await r.text(".ask dl"), "regrow undo does not restore a volume", "the volume line")
            let tries: @MainActor () async -> Void = {
                await r.click("#go")
                await r.click("#go", force: true)
                for target in ["#read", "#go", "body"] {
                    await r.focus(target)
                    await r.pressReturn()
                }
            }
            await tries()
            // Ticked, with the answer still out: still locked.
            await r.click("#read")
            r.expect(await r.flag("return document.getElementById('read').checked && document.getElementById('go').disabled"), "the run button opens before macOS has answered")
            await tries()
            await r.click("#read")
            r.send(["event": "shell", "state": "automation", "allowed": true])
            await r.settle()
            r.has(await r.text(".ask dl"), "Free once you empty it", "the Trash line once regrow may control Finder")
            await tries()
            r.same(r.sent("execute").count, 0, "executes from the locked confirm step")
            r.same(await r.screen(), "confirm", "the screen")
            r.same(await r.text(".ask h2"), "Run \(actions.count) actions?", "the question")
            r.has(await r.text(".gate label"), "I read the \(actions.filter { $0.str("kind") != "trash" }.count) commands. They have no undo.", "the tick box's sentence")
            r.has(await r.text(".ask dl"), "empties the Trash", "the line on the command that empties the Trash")
            if windowSize.width == 1096 {
                await drawn(r, [
                    (".ask", "max-width", "560px"), (".ask h2", "font-size", "28px"), (".ask h2", "font-family", "ui-serif"),
                    (".btn.run", "border-top-left-radius", "8px"), (".top .status", "#height", "0"),
                ])
            }
            await r.shoot()
        },
        Scenario(name: "confirm-ready", play: Play(executeStops: true)) { r in
            await toConfirm(r)
            await r.click("#read")
            r.expect(!(await r.flag("return document.getElementById('go').disabled")), "the run button stays locked once ticked and answered")
            await r.shoot()
            // Return does nothing here, wherever the focus is; a real Return on this button would otherwise press it.
            for target in ["#read", "#go", "body"] {
                await r.focus(target)
                await r.pressReturn()
            }
            r.same(r.sent("execute").count, 0, "executes sent by Return")
            r.same(await r.screen(), "confirm", "the screen after Return")
            // One click runs the plan once; a second click in the same breath finds nothing to run.
            await r.js("const go = document.getElementById('go'); go.click(); go.click(); return null")
            await r.settle()
            await r.click("#go", force: true)
            r.same(r.sent("execute").count, 1, "executes sent by two clicks")
            r.same(r.sent("execute").first?.str("plan_id") ?? "", r.planID, "the plan executed")
            r.same(await r.screen(), "running", "the screen after the click")
        },
        Scenario(name: "confirm-staging", play: Play(automation: false)) { r in
            await toConfirm(r)
            let line = await r.text(".ask dl")
            r.has(line, "moves to regrow staging", "the Trash line when regrow may not control Finder")
            r.has(line, "regrow undo restores them", "the Trash line")
            r.has(line, "emptying the Trash does not free them", "the Trash line")
            await r.click("#read")
            r.expect(!(await r.flag("return document.getElementById('go').disabled")), "the run button stays locked when the moves go to staging")
            await r.shoot()
        },
        Scenario(name: "running", play: Play(executeStops: true)) { r in
            await toRun(r)
            let journal = r.transcript.events("execute", "journal").map { $0.obj("entry") }
            let half = Array(journal.prefix(r.transcript.lines("execute").count / 2))
            let actions = r.transcript.event("plan-all", "plan").obj("plan").objs("actions")
            let ended = { (event: String) in half.filter { $0.str("event") == event }.count }
            r.same(await r.screen(), "running", "the screen")
            r.same(await r.count(".ln[data-state=done]"), ended("done"), "lines marked done")
            r.same(await r.count(".ln[data-state=fail]"), ended("fail"), "lines marked failed")
            r.same(await r.count(".ln[data-state=running]"), ended("start") - ended("done") - ended("fail"), "lines marked running")
            r.same(await r.count(".ln[data-state=waiting]"), actions.count - ended("start"), "lines marked waiting")
            r.same(await r.count(".ln"), actions.count, "lines, one for each action")
            r.has(await r.text(".lead h2"), "\(ended("done")) of \(actions.count) actions done, \(ended("fail")) failed", "the headline")
            let error = half.first { $0.str("event") == "fail" }?.str("error") ?? ""
            r.same(await r.js("return document.querySelector('.ln[data-state=fail] .err').textContent") as? String ?? "", error, "the failure's error text")
            r.expect(await r.flag("const now = document.querySelector('.ln[data-state=running]').getBoundingClientRect(); return now.top > 42 && now.bottom < 580"), "the running action is out of sight")
            await r.shoot()
            await r.click("[data-do=halt]")
            r.same(r.sent("cancel").count, 1, "cancels sent by Stop after this action")
            r.same(r.sent("cancel").first?.str("target") ?? "", r.executeRequest, "the cancel's target")
            await r.click("[data-do=halt]", force: true)
            r.same(r.sent("cancel").count, 1, "cancels sent by a second click")
        },
        Scenario(name: "result") { r in
            await toRun(r)
            let result = r.transcript.event("execute", "done").obj("result"), actions = r.transcript.event("plan-all", "plan").obj("plan").objs("actions")
            let journal = r.transcript.events("execute", "journal").map { $0.obj("entry") }
            r.same(await r.screen(), "result", "the screen")
            r.same(await r.text(".lead h2"), "The run has ended: \(result.int("done")) of \(actions.count) actions done, \(result.int("failed")) failed.", "the headline")
            let say = await r.text(".lead .say")
            r.has(say, human(result.int("bytes") - result.int("trash_bytes") - result.int("staged_bytes") - result.int("exported_bytes")) + " is free now", "the figures")
            r.has(say, human(result.int("trash_bytes")) + " is in the Trash", "the figures")
            r.has(say, human(result.int("staged_bytes")) + " is in regrow staging", "the figures")
            r.has(say, "emptying the Trash does not free", "what staging means")
            r.has(say, human(result.int("exported_bytes")) + " of removed Docker volumes is kept as tarballs", "the figures")
            r.has(say, "regrow undo does not restore them", "what an export means")
            r.has(say, "in the run's folder of regrow staging", "where the exports are")
            let staging = r.transcript.event("execute", "done").str("staging_dir")
            r.expect(!staging.isEmpty, "the recorded done names no staging folder")
            r.same(await r.text(".folder .mono"), staging.replacingOccurrences(of: "/Users/dev/", with: "~/"), "the run's folder, drawn with ~")
            r.same(await r.text(".undo .mono"), "\(r.transcript.event("hello", "hello").str("self")) undo \(result.str("run_id"))", "the undo line: the engine's own command")
            r.expect(await r.flag("return getComputedStyle(document.querySelector('.undo .mono')).webkitUserSelect === 'text'"), "the undo command cannot be selected")
            r.same(await r.texts(".sec.bad .err"), journal.filter { $0.str("event") == "fail" }.map { $0.str("error").replacingOccurrences(of: "\\s+", with: " ", options: .regularExpression) }, "what failed")
            r.same(await r.count(".ln[data-state=done]"), journal.filter { $0.str("event") == "done" }.count, "lines marked done")
            r.same(await r.js("return [...document.querySelectorAll(':is(#top, #page, #tray) [data-do]')].map((el) => el.dataset.do).filter((d) => d !== 'copy')") as? [String] ?? [], ["scan"], "the ways on from a result")
            await r.shoot()
            // The undo command and the run's folder are copied whole: the folder with its home, not ~.
            await r.click(".undo .copy")
            r.same(r.pasted.last ?? "", "\(r.transcript.event("hello", "hello").str("self")) undo \(result.str("run_id"))", "what Copy beside the undo command put on the pasteboard")
            r.same(await r.js("return document.querySelector('.undo .copy').textContent") as? String ?? "", "Copied", "the undo command's Copy once clicked")
            await r.click(".folder .copy")
            r.same(r.pasted.last ?? "", staging, "what Copy beside the run's folder put on the pasteboard")
            r.same(await r.count(".sec.bad .ln .copy"), await r.count(".sec.bad .ln"), "failed lines without a Copy")
            await r.click("[data-do=scan]")
            r.same(r.sent("scan").count, 2, "scans after Scan again")
            r.same(await r.screen(), "findings", "the screen after Scan again")
        },
        Scenario(name: "result-skipped", play: Play(execute: "execute-trash-failed")) { r in
            await toRun(r)
            let result = r.transcript.event("execute-trash-failed", "done").obj("result"), skipped = result.strs("skipped")
            let actions = r.transcript.event("plan-all", "plan").obj("plan").objs("actions").count
            r.has(await r.text(".lead h2"), "\(result.int("done")) of \(actions) actions done, \(result.int("failed")) failed, \(skipped.count) skipped", "the headline")
            r.same(await r.count(".ln[data-state=skipped]"), skipped.count, "lines marked skipped")
            r.same(await r.count(".sec.bad .act"), Int(result.int("failed")), "rows under Failed")
            r.has(await r.text(".act[data-rule=node-modules-dirs]"), "Skipped: emptying the Trash failed", "the reason for a skip")
            r.same(await r.count(".undo"), 0, "undo lines when nothing went to the Trash or to staging")
            r.has(await r.text(".lead .say"), human(result.int("bytes") - result.int("exported_bytes")) + " is free now", "the figures")
            r.has(await r.text(".lead .say"), human(result.int("exported_bytes")) + " of removed Docker volumes is kept as tarballs", "the volume exports")
            await r.shoot()
            await r.shoot("-list", top: ".act[data-rule=xcode-derived-data]")
        },
        Scenario(name: "canceled-scan", play: Play(tick: nil, scanStops: true)) { r in
            await r.click("[data-do=stop]")
            let all = r.transcript.events("scan", "finding").count
            r.same(r.sent("cancel").count, 1, "cancels sent by Stop")
            r.same(r.sent("cancel").first?.str("target") ?? "", r.sent("scan").first?.str("id") ?? "-", "the cancel's target")
            r.same(await r.phase(), "canceled", "the scan's phase")
            r.has(await r.text(".top .status"), "stopped after \(all / 2) of \(all) rules", "the status")
            r.same(await r.texts(".four b"), Array(repeating: "not counted", count: 4), "the four totals")
            r.same(r.sent("plan").count, 0, "plan requests for a canceled scan")
            r.same(await r.count("input[data-tick]:not(:disabled)"), 0, "tick boxes that can be changed on a canceled scan")
            r.same(await r.text(".tray .btn"), "Scan again", "the tray's action")
            // No tick has come from the shell in this scenario.
            r.has(await r.text(".lead .aside"), "has not sampled free space", "why there is no forecast")
            await r.shoot()
            r.play.scanStops = false
            await r.click(".tray [data-do=scan]")
            r.same(r.sent("scan").count, 2, "scans after Scan again")
            r.same(await r.phase(), "done", "the second scan's phase")
        },
        Scenario(name: "scan-out-of-date") { r in
            await results(r)
            let id = r.transcript.event("scan", "start").str("scan_id")
            r.play.planError = ("scan_spent", "an execute already ran against scan \(id): scan again")
            let before = r.sent("plan").count
            await r.click("[data-do=review]")
            r.same(r.sent("plan").count, before + 1, "plan requests: the one refused and no retry")
            r.same(await r.screen(), "findings", "the screen")
            let tray = await r.text(".tray .say")
            r.has(tray, "This scan is out of date", "the tray")
            r.has(tray, "an execute already ran against scan \(id)", "the engine's message")
            r.same(await r.text(".tray .btn"), "Scan again", "the tray's action")
            r.same(await r.count("input[data-tick]:not(:disabled)"), 0, "tick boxes that can be changed on a scan that is out of date")
            r.same(await r.count("[data-do=review]"), 0, "ways to plan a scan that is out of date")
            await r.shoot()
        },
        Scenario(name: "execute-refused") { r in
            // The engine refuses the execute: back to the plan with a fresh one, and nothing is sent twice.
            await toConfirm(r)
            await r.click("#read")
            let plans = r.sent("plan").count
            r.play.executeError = ("plan_expired", "plan \(r.planID) expired: plan again")
            await r.click("#go")
            r.same(r.sent("execute").count, 1, "executes sent")
            r.same(r.sent("plan").count, plans + 1, "plan requests after the refusal")
            r.same(r.sent("plan").last?.strs("select").sorted() ?? [], r.transcript.atoms("scan").sorted(), "the atoms of the fresh plan")
            r.same(await r.screen(), "plan", "the screen after the refusal")
            r.has(await r.text(".lead .aside"), "did not run, so this is a fresh one. Nothing has run.", "why the plan is a fresh one")
            r.same(await r.text(".tray .btn:not(.quiet)"), "Run plan…", "the tray's action on the fresh plan")
            await r.shoot()
        },
        Scenario(name: "stale-review") { r in
            // A fresh plan is asked for, the owner goes back and touches the ticks before it comes, and asks again:
            // the late answer to the first request is not the plan drawn, and the answer to the second is. The two
            // answers differ only by the request they answer, which is all the page goes by.
            await toPlan(r)
            await r.jump(minutes: 11)
            await r.wait(for: "Plan again on a plan that is too old", "!!document.querySelector('[data-do=again]')")
            r.play.holdPlan = true
            r.expect(await r.click("[data-do=again]"), "there is no Plan again to click")
            r.expect(await r.click("[data-do=back]"), "there is no Back to click")
            r.same(await r.screen(), "findings", "the screen after Back")
            // Unticked and ticked again, so the transcript has a plan for the second request.
            r.play.leaveOpen = true
            let box = await r.js("const b = document.querySelector('input[data-tick]:checked:not([data-item])'); b.click(); return b.dataset.tick") as? String ?? ""
            await r.settle()
            await r.js("document.querySelector(`input[data-tick=\"${tick}\"]:not([data-item])`).click(); return null", ["tick": box])
            await r.settle()
            r.play.holdPlan = true
            r.expect(await r.click("[data-do=review]"), "there is no Review plan to click")
            let reviews = r.sent("plan").count
            r.releasePlan()
            await r.settle()
            r.same(await r.screen(), "findings", "the screen once the first request's answer came")
            r.same(await r.text("#next"), "Planning", "the tray's action while the second request is out")
            r.same(r.sent("plan").count, reviews, "plan requests after the late answer")
            await r.shoot()
            r.releasePlan()
            await r.settle()
            r.same(await r.screen(), "plan", "the screen once the second request's answer came")
            r.same(r.sent("execute").count, 0, "executes sent")
        },
        Scenario(name: "hello-again") { r in
            // The shell repeats the engine's hello when the page says it is ready: one hello, one scan.
            await results(r)
            let scans = r.sent("scan").count
            r.send([r.transcript.lines("hello")[0]])
            await r.settle()
            r.same(r.sent("scan").count, scans, "scans after the same hello came again")
            await results(r)
            await r.shoot()
        },
        Scenario(name: "opened", play: Play(opened: true)) { r in
            // Open shows the window and starts nothing: the tray offers the scan.
            r.same(r.sent("scan").count, 0, "scans in a window opened with Open")
            r.same(await r.text(".lead h2"), "No scan to show.", "the headline")
            r.same(await r.text(".tray .say"), "A scan reads the disk and removes nothing.", "the tray")
            r.same(await r.count(".top [data-do=scan]"), 0, "Scan again in the top bar with no scan")
            await r.shoot()
            await r.click(".tray [data-do=scan]")
            await r.settle()
            r.same(r.sent("scan").count, 1, "scans after the tray's Scan")
            await results(r)
        },
        Scenario(name: "scan-before-hello", play: Play(scanFirst: true)) { r in
            // The menu's Scan reaches a page whose engine has not said hello yet: the scan waits for it.
            await results(r)
            r.same(r.sent("scan").count, 1, "scans")
            await r.shoot()
        },
        Scenario(name: "scan-asked-again") { r in
            // The menu's Scan with the window open on a scan: a new one starts.
            await results(r)
            r.send(["event": "shell", "state": "scan"])
            await r.settle()
            r.same(r.sent("scan").count, 2, "scans after the menu's Scan")
            await results(r)
            await r.shoot()
        },
        Scenario(name: "pre-hello", play: Play(opened: true, holdHello: true)) { r in
            // Open before the engine has said hello: nothing is promised, and Scan waits for the engine.
            r.same(await r.text(".lead h2"), "Waiting for regrow.", "the headline")
            r.same(await r.text(".tray .say"), "Waiting for the engine to start.", "the tray")
            r.expect(await r.flag("return document.querySelector('.tray [data-do=scan]').disabled"), "Scan can be clicked before the engine said hello")
            await r.shoot()
            r.send([r.transcript.lines("hello")[0]])
            await r.settle()
            r.same(r.sent("scan").count, 0, "scans after hello in a window opened with Open")
            r.same(await r.text(".tray .say"), "A scan reads the disk and removes nothing.", "the tray after hello")
        },
        Scenario(name: "asked-pre-hello", play: Play(scanFirst: true, holdHello: true)) { r in
            // The relaunch that applies Full Disk Access, before the engine has said hello: the scan waits for it.
            r.same(await r.text(".tray .say"), "The scan starts once the engine is ready.", "the tray with a scan asked for")
            r.expect(await r.flag("return document.querySelector('.tray [data-do=scan]').disabled"), "Scan can be clicked before the engine said hello")
            r.same(r.sent("scan").count, 0, "scans before hello")
            await r.shoot()
            r.send([r.transcript.lines("hello")[0]])
            await r.settle()
            r.same(r.sent("scan").count, 1, "scans after hello")
            await results(r)
        },
        Scenario(name: "down-no-scan", play: Play(opened: true)) { r in
            // The engine stops under a window with no scan: none is promised, and none starts when it is back.
            r.send(["event": "shell", "state": "engine_down", "message": "the engine exited with status 2"])
            await r.settle()
            r.same(await r.text(".lead .aside"), "Scan once the engine is back.", "the engine-down screen")
            await r.shoot()
            r.send([r.transcript.lines("hello")[0]])
            await r.settle()
            r.same(r.sent("scan").count, 0, "scans once the engine is back")
            r.same(await r.text(".lead h2"), "No scan to show.", "the headline once the engine is back")
        },
        Scenario(name: "scan-while-down", play: Play(opened: true)) { r in
            // The menu's Scan while the engine is down: the scan starts when it is back.
            r.send(["event": "shell", "state": "engine_down", "message": "the engine exited with status 2"])
            await r.settle()
            r.send(["event": "shell", "state": "scan"])
            await r.settle()
            r.same(await r.text(".lead .aside"), "The scan you asked for starts as soon as the engine is back.", "the engine-down screen")
            r.same(r.sent("scan").count, 0, "scans while the engine is down")
            r.send([r.transcript.lines("hello")[0]])
            await r.settle()
            r.same(r.sent("scan").count, 1, "scans once the engine is back")
            await results(r)
            await r.shoot()
        },
        Scenario(name: "reloaded", play: Play(opened: true)) { r in
            // The page was loaded again after its process ended: it says what it showed is gone.
            r.send(["event": "shell", "state": "reloaded", "running": false])
            await r.settle()
            r.same(await r.text(".lead .aside"), "This window's page stopped and was loaded again, so what it showed is gone.", "the note")
            await r.shoot()
            await r.click(".tray [data-do=scan]")
            await r.settle()
            await results(r)
            r.same(await r.count(".lead .aside"), 0, "the note once a scan started")
        },
        Scenario(name: "reloaded-run", play: Play(helloSelf: "/Users/dev/Team's R&D/Regrow.app/Contents/MacOS/regrow-engine", opened: true)) { r in
            // Loaded again while a run was going: the run went on out of sight, however it ended, and history lists what it did.
            r.send(["event": "shell", "state": "reloaded", "running": true])
            await r.settle()
            let note = await r.text(".lead .aside")
            r.has(note, "so what it showed is gone. A run was going when it stopped; ", "the note")
            r.has(note, " lists what it did.", "the note's end")
            let history = "'/Users/dev/Team'\\''s R&D/Regrow.app/Contents/MacOS/regrow-engine' history"
            r.same(await r.text(".lead .aside .mono"), history, "the command in the note")
            await r.click(".lead .aside .copy")
            await r.settle()
            r.same(r.pasted.last ?? "", history, "what the note's Copy copies")
            await r.shoot()
        },
        Scenario(name: "review-error") { r in
            // A review sent before the last tick change was counted is refused: the count still goes out, and the
            // tray says what the ticks free.
            await results(r)
            r.play.planError = ("unmatched", "selectors matched nothing in this scan: sim-devices/AAA-111")
            await r.js("""
                for (let i = 0; i < 99; i++) {
                  const box = [...document.querySelectorAll('input[data-tick]:not([data-item])')].find((b) => !b.checked && !b.disabled);
                  if (!box) break;
                  box.click();
                }
                document.querySelector('[data-do=review]').click();
                return null;
                """)
            await r.settle()
            r.has(await r.text(".note"), "selectors matched nothing in this scan: sim-devices/AAA-111", "the engine's message")
            r.same(await r.screen(), "findings", "the screen")
            let frees = human(r.transcript.event("plan-all", "plan").obj("totals").int("frees_now"))
            r.has(await r.text(".tray .say"), frees + " frees now", "the tray after the engine refused the review")
            let exported = human(r.transcript.event("plan-all", "plan").obj("totals").int("exported"))
            r.has(await r.text(".tray .say"), exported + " in volume exports", "the tray names the volume exports the ticks hold")
            // The tray grows past 60 px for its lines on a narrow window (PAGECHECK_SIZE=916x520 draws one).
            r.expect(await r.flag("""
                const tray = document.querySelector('.tray').getBoundingClientRect();
                return [...document.querySelectorAll('.tray > *')].every((el) => {
                  const box = el.getBoundingClientRect();
                  return box.top >= tray.top - 0.5 && box.bottom <= tray.bottom + 0.5;
                });
                """), "every part of the tray stays inside it")
            await r.shoot()
        },
        Scenario(name: "other-error") { r in
            // An error the page has no state for is shown with the engine's message.
            await results(r)
            r.play.planError = ("unmatched", "selectors matched nothing in this scan: sim-devices/AAA-111")
            await tickAll(r)
            r.has(await r.text(".note"), "selectors matched nothing in this scan: sim-devices/AAA-111", "the engine's message")
            r.expect(!(await r.text(".tray .say")).contains("counting"), "the tray still counts after the engine refused")
            r.same(await r.screen(), "findings", "the screen")
            await r.shoot()
        },
        Scenario(name: "engine-down-run", play: Play(executeStops: true)) { r in
            // The engine exits under a run: what had been done is said, and stays said after the next scan.
            await toRun(r)
            let done = r.transcript.events("execute", "journal").prefix(r.transcript.lines("execute").count / 2).filter { $0.obj("entry").str("event") == "done" }.count
            r.send(["event": "shell", "state": "engine_down", "message": "the engine exited with status 2"])
            await r.settle()
            r.same(await r.screen(), "down", "the screen")
            r.has(await r.text(".note"), "The engine stopped during the run, after reporting \(done) of 25 actions done.", "what the run had done")
            r.same(await r.text(".note .mono"), "\(r.transcript.event("hello", "hello").str("self")) undo \(r.planID)", "the undo command")
            r.has(await r.text(".note"), "an item it was moving as it stopped may be in the Trash or in regrow staging, and undo does not know of it", "the move that may have been in flight")
            await r.shoot()
            r.send([r.transcript.lines("hello")[0]])
            await r.settle()
            r.same(await r.screen(), "findings", "the screen after the engine came back")
            r.has(await r.text(".note"), "The engine stopped during the run, after reporting \(done) of 25 actions done", "what the run had done, after the next scan")
        },
        Scenario(name: "engine-down-commands", play: Play(executeLines: 1, commandsOnly: true)) { r in
            // The engine exits under a plan of commands only: none has an undo, so the note offers none.
            await toRun(r)
            let commands = r.transcript.event("plan-all", "plan").obj("plan").objs("actions").filter { $0.str("kind") != "trash" }.count
            r.send(["event": "shell", "state": "engine_down", "message": "the engine exited with status 2"])
            await r.settle()
            r.same(await r.text(".note"), "The engine stopped during the run, after reporting 0 of \(commands) actions done.", "what the run had done")
            r.same(await r.count(".note .mono"), 0, "commands in the note for a plan with no Trash move")
            await r.shoot()
        },
        Scenario(name: "engine-down-first-command", play: Play(executeLines: 1)) { r in
            // Only the first action's start, a command, was reported: nothing is done, and the plan still moves.
            await stoppedNote(r, "execute", 1)
        },
        Scenario(name: "engine-down-skipped-moves", play: Play(execute: "execute-trash-failed", executeLines: 13)) { r in
            // Emptying the Trash failed, so the engine skipped the moves after it without journaling them, then
            // stopped during a later command: the first unmarked action is a move that never ran.
            await stoppedNote(r, "execute-trash-failed", 13)
        },
        Scenario(name: "undo-quoted", play: Play(helloSelf: "/Users/dev/R&D/Regrow.app/Contents/MacOS/regrow-engine")) { r in
            // An app's engine at a path the shell would cut short with no space in it: the undo command quotes it.
            await toRun(r)
            let run = r.transcript.event("execute", "done").obj("result").str("run_id")
            r.same(await r.text(".undo .mono"), "'/Users/dev/R&D/Regrow.app/Contents/MacOS/regrow-engine' undo \(run)", "the undo command")
            await r.shoot()
        },
        Scenario(name: "undo-quoted-note", play: Play(executeLines: 25, helloSelf: "/Users/dev/Team's R&D/Regrow.app/Contents/MacOS/regrow-engine")) { r in
            // The stopped-run notice quotes the same way, a single quote included.
            await toRun(r)
            r.send(["event": "shell", "state": "engine_down", "message": "the engine exited with status 2"])
            await r.settle()
            r.same(await r.text(".note .mono"), "'/Users/dev/Team'\\''s R&D/Regrow.app/Contents/MacOS/regrow-engine' undo \(r.planID)", "the undo command in the notice")
            await r.shoot()
        },
        Scenario(name: "engine-down") { r in
            await results(r)
            r.send(["event": "shell", "state": "engine_down", "message": "the engine exited with status 1: write /dev/stdout: broken pipe"])
            await r.settle()
            r.same(await r.screen(), "down", "the screen")
            r.has(await r.text(".page"), "the engine exited with status 1: write /dev/stdout: broken pipe", "the shell's message")
            r.same(await r.text(".lead .aside"), "This page scans again as soon as the engine is back.", "the engine-down screen")
            r.same(await r.count(":is(#top, #page, #tray) button:not(:disabled)"), 0, "buttons in the scan that can be pressed while the engine is down")
            await r.shoot()
            let scans = r.sent("scan").count
            r.send([r.transcript.lines("hello")[0]])
            await r.settle()
            r.same(r.sent("scan").count, scans + 1, "scans after the engine came back")
            r.same(await r.screen(), "findings", "the screen after the engine came back")
        },
    ]
    // Results, plan and confirm also in dark.
    return lit + lit.filter { ["results", "plan", "confirm", "general", "stats"].contains($0.name) }.map { var dark = $0; dark.dark = true; return dark }
}()

// MARK: - Main

@MainActor func runAll() async -> Int32 {
    let arguments = CommandLine.arguments
    guard arguments.count == 4, let transcript = try? Transcript(path: arguments[2]) else {
        FileHandle.standardError.write(Data("usage: pagecheck <page.html> <transcript.jsonl> <out dir>\n".utf8))
        return 64
    }
    let page = URL(fileURLWithPath: arguments[1]), out = URL(fileURLWithPath: arguments[3], isDirectory: true)
    try? FileManager.default.createDirectory(at: out, withIntermediateDirectories: true)
    var failures: [String] = [], checks = 0, shots = 0
    let only = (ProcessInfo.processInfo.environment["PAGECHECK_ONLY"] ?? "").split(separator: ",").map(String.init)
    let chosen = scenarios.filter { only.isEmpty || only.contains($0.name) }
    for scenario in chosen {
        let run = Run(name: scenario.name, dark: scenario.dark, transcript: transcript, out: out, play: scenario.play)
        await run.load(page)
        let paper = await run.paper()
        run.expect(paper.hasPrefix("rgb(2") != scenario.dark, "the page did not follow the \(scenario.dark ? "dark" : "light") theme: its paper is \(paper)")
        await scenario.run(run)
        await run.settle()
        run.expect(!run.shots.isEmpty, "no PNG was written")
        print("\(run.failures.isEmpty ? "ok  " : "FAIL") \(run.name): \(run.checks) checks, \(run.shots.map { URL(fileURLWithPath: $0).lastPathComponent }.joined(separator: " "))")
        failures += run.failures
        checks += run.checks
        shots += run.shots.count
        run.close()
    }
    for failure in failures { print("FAIL \(failure)") }
    print("pagecheck: \(chosen.count) scenarios, \(checks) checks, \(shots) PNGs in \(out.path), \(failures.count) failed")
    return failures.isEmpty ? 0 : 1
}

setvbuf(stdout, nil, _IOLBF, 0)
let app = NSApplication.shared
app.setActivationPolicy(.accessory)
Task { @MainActor in exit(await runAll()) }
DispatchQueue.main.asyncAfter(deadline: .now() + 900) {
    print("pagecheck: timed out")
    exit(2)
}
app.run()
