import AppKit
import RegrowKit
import Security
import ServiceManagement

/// The app behind the menu: it keeps one engine running, ticks, routes
/// between the engine and the page, and holds what the menu shows.
@MainActor
final class Shell: ObservableObject {
    static let shared = Shell()

    private static let autotrimKey = "autotrim"
    private static let loginItemKey = "loginItemRegistered"
    private static let tickPrefix = "\(Router.shellPrefix)tick-"

    @Published private(set) var tick: Wire.Tick?
    @Published private(set) var checkedAt: Date?
    @Published private(set) var tickFailure: TickFailure?
    @Published private(set) var hello: Wire.Hello?
    @Published private(set) var environmentFailure: String?
    @Published private(set) var engineDown: String?
    @Published var autotrim: Bool {
        didSet {
            guard autotrim != oldValue else { return }
            UserDefaults.standard.set(autotrim, forKey: Self.autotrimKey)
            log("autotrim turned \(autotrim ? "on" : "off")")
            watch?.tickNow()
        }
    }

    private let config = Config.load()
    private let log: LogFile
    private let notifier: Notifier

    private var router = Router()
    private var supervisor: EngineSupervisor?
    private var watch: Watch?
    private var window: ScanWindow?
    private var stateDirectory: URL?
    private var helloLine: String?
    /// The settings the page was last handed.
    private var settings: WindowSettings?
    /// A read of the settings is under way since then; another asked
    /// meanwhile runs once it ends, or at once after 10 s, in case macOS
    /// never answers that one. Only the newest read counts.
    private var readingSettings: Date?
    private var readSettingsAgain = false
    private var settingsSerial = 0
    private var tickLine: String?
    private var downLine: String?
    private var lockNoticed = false
    private var stopping = false
    private var instanceLock: InstanceLock?
    private var askingFinder = false
    private var pageReady = false
    /// A scan asked for before the page said it was ready.
    private var scanAsked = false
    /// The page was loaded again after its process ended, and lost what it showed.
    private var pageReloaded = false
    /// An execute was in flight when the reloaded page's process ended; it runs on.
    private var reloadedMidRun = false
    private var update: AppUpdate.Phase = .unchecked
    /// The newer release the last check found.
    private var updateRelease: AppUpdate.Release?
    /// A relaunch's or the update's helper is started and the app is quitting.
    private var handedOff = false

    private init() {
        log = LogFile(url: config.log)
        notifier = Notifier(log: log)
        autotrim = UserDefaults.standard.bool(forKey: Self.autotrimKey)
    }

    // MARK: What the menu shows

    var barTitle: String {
        tick.map { humanBytes($0.free) } ?? "…"
    }

    private static let clock: DateFormatter = {
        let formatter = DateFormatter()
        formatter.dateStyle = .none
        formatter.timeStyle = .short
        return formatter
    }()

    var menuState: MenuState {
        MenuState(
            tick: tick, checkedAt: checkedAt.map(Self.clock.string(from:)), tickFailure: tickFailure,
            engineDown: engineDown, autotrim: autotrim, fda: hello?.fda,
            environmentFailure: environmentFailure, engine: engine,
            loginItem: loginItemState)
    }

    var engine: String {
        config.engine
    }

    // MARK: Launch

    /// A launch at login opens no window; any other is the owner opening
    /// the app.
    func start(atLogin reason: String?) {
        log("launched from \(Bundle.main.bundlePath), pid \(getpid())\(reason.map { ", at login: \($0)" } ?? "")")
        if Config.installed {
            notifier.askPermission()
            registerLoginItem()
        } else {
            log("not ~/Applications/Regrow.app: asking for no notification permission and registering no login item")
        }
        let log = self.log
        DispatchQueue.global(qos: .userInitiated).async {
            let began = Date()
            let captured = LoginEnvironment.capture()
            let answered = Date()
            log("the login shell answered in \(Self.milliseconds(since: began)) ms")
            DispatchQueue.main.async {
                MainActor.assumeIsolated {
                    let waited = Self.milliseconds(since: answered)
                    if waited >= 1000 {
                        log("the main thread took \(waited) ms to take the login shell's answer")
                    }
                    self.environmentRead(captured)
                }
            }
        }
        if config.openScan || reason == nil {
            showWindow(scan: config.openScan)
        }
    }

    private nonisolated static func milliseconds(since start: Date) -> Int {
        Int(Date().timeIntervalSince(start) * 1000)
    }

    /// A login item the owner removed in System Settings stays removed:
    /// the app registers itself once.
    private func registerLoginItem() {
        guard !UserDefaults.standard.bool(forKey: Self.loginItemKey) else { return }
        do {
            try SMAppService.mainApp.register()
            UserDefaults.standard.set(true, forKey: Self.loginItemKey)
            log("registered as a login item")
        } catch {
            log("could not register as a login item: \(error.localizedDescription)")
        }
    }

    /// Read from macOS at each menu open: the owner may change it in System Settings.
    private var loginItemState: LoginItemState? {
        guard Config.installed else { return nil }
        switch SMAppService.mainApp.status {
        case .enabled: return .on
        // macOS reports a never-registered or unregistered app as notFound too.
        case .notRegistered, .notFound: return .off
        case .requiresApproval: return .needsApproval
        @unknown default: return nil
        }
    }

    func chooseLoginItem(_ choice: LoginItemChoice) {
        // A copy outside ~/Applications would register that copy.
        guard Config.installed else {
            log("not ~/Applications/Regrow.app: no login item to change")
            return
        }
        // Either way the owner has chosen, so the launch does not register again.
        UserDefaults.standard.set(true, forKey: Self.loginItemKey)
        do {
            switch choice {
            case .register:
                try SMAppService.mainApp.register()
                log("registered as a login item from the menu")
            case .unregister:
                try SMAppService.mainApp.unregister()
                log("removed as a login item from the menu")
            case .openSettings:
                log("opening Login Items settings")
                SMAppService.openSystemSettingsLoginItems()
            }
        } catch {
            log("could not change the login item: \(error.localizedDescription)")
        }
        objectWillChange.send()
        readSettings()
    }

    /// Reads the grants off the main thread and hands the page the
    /// settings when they changed. The owner may change any of them in
    /// System Settings, so they are read again each time the window
    /// comes to the front.
    private func readSettings() {
        if let since = readingSettings, Date().timeIntervalSince(since) < 10 {
            readSettingsAgain = true
            return
        }
        readingSettings = Date()
        settingsSerial += 1
        let serial = settingsSerial
        let notifier = self.notifier, installed = Config.installed
        DispatchQueue.global(qos: .userInitiated).async {
            let finder = Automation.finderGrant()
            // A copy elsewhere shares the installed app's bundle id, and with
            // it the grant, but posts nothing.
            guard installed else {
                DispatchQueue.main.async {
                    MainActor.assumeIsolated {
                        self.settingsRead(serial, notifications: .unknown, finder: finder)
                    }
                }
                return
            }
            notifier.grant { notifications in
                DispatchQueue.main.async {
                    MainActor.assumeIsolated {
                        self.settingsRead(serial, notifications: notifications, finder: finder)
                    }
                }
            }
        }
    }

    private func settingsRead(_ serial: Int, notifications: WindowSettings.Grant, finder: WindowSettings.Grant) {
        guard serial == settingsSerial else { return }
        readingSettings = nil
        if readSettingsAgain {
            readSettingsAgain = false
            readSettings()
        }
        let read = WindowSettings(
            loginItem: loginItemState, notifications: notifications, finder: finder, log: config.log.path,
            staging: stateDirectory?.appendingPathComponent("staging").path)
        guard read != settings else { return }
        settings = read
        if pageReady {
            window?.deliver(read.line)
        }
    }

    private func environmentRead(_ captured: Result<[String: String], LoginEnvironment.Failure>) {
        let base: [String: String]
        switch captured {
        case .success(let environment):
            base = environment
            log("environment captured from \(LoginEnvironment.loginShell()): \(environment.count) variables")
        case .failure(let failure):
            base = Account.environment()
            environmentFailure = failure.reason
            log("environment not captured: \(failure.reason)")
            window?.deliver(environmentLine(failure.reason))
        }
        let environment = config.engineEnvironment(over: base)
        let state = PidFile.stateDirectory(environment: environment)
        stateDirectory = state

        let engine = config.engine
        let app = Bundle.main.executablePath ?? CommandLine.arguments[0]
        let log = self.log
        DispatchQueue.global(qos: .userInitiated).async {
            let lock: InstanceLock?
            do {
                lock = try InstanceLock.acquire(in: state)
                if lock == nil {
                    log("regrow is already running on \(state.path); this one quits")
                    // This copy holds nothing to stop, so a signal ends it
                    // at once, also while nobody answers the alert.
                    signal(SIGTERM, SIG_DFL)
                    signal(SIGINT, SIG_DFL)
                    MainRunLoop.perform {
                        let alert = NSAlert()
                        alert.messageText = "regrow is already running"
                        alert.informativeText = "Another copy of regrow uses \(state.path), so this one quits."
                        NSApp.activate(ignoringOtherApps: true)
                        alert.runModal()
                        exit(0)
                    }
                    return
                }
                Self.clearEarlierRun(state: state, engine: engine, app: app, log: log)
            } catch {
                lock = nil
                log("no single-instance check, so no engine an earlier run left is stopped: \(error)")
            }
            DispatchQueue.main.async {
                MainActor.assumeIsolated {
                    self.instanceLock = lock
                    self.startEngine(environment)
                }
            }
        }
    }

    /// Stops an engine that outlived the app that started it. Called with
    /// the instance lock held; the app the pid file names can still run
    /// all the same if it ran without the lock, and then keeps its engine.
    private nonisolated static func clearEarlierRun(state: URL, engine: String, app: String, log: LogFile) {
        guard let earlier = PidFile.read(from: state), earlier.app != getpid() else { return }
        if EarlierProcess.isRunning(earlier.app, executable: app) {
            log("the app the pid file names, pid \(earlier.app), still runs; its engine is left alone")
            return
        }
        switch EarlierProcess.stop(earlier.engine, executable: engine) {
        case .notOurs:
            break
        case .stopped(let rung):
            log("an engine left by an earlier run, pid \(earlier.engine), was stopped by \(rung.rawValue)")
        case .survived:
            log("an engine left by an earlier run, pid \(earlier.engine), is still running after SIGKILL")
        }
    }

    private func startEngine(_ environment: [String: String]) {
        guard !stopping else { return }
        let supervisor = EngineSupervisor(
            launch: .init(
                executable: config.engine, arguments: config.engineArguments,
                environment: environment, log: config.engineLog),
            queue: .main,
            onStart: { pid in MainActor.assumeIsolated { self.engineStarted(pid) } },
            onLine: { line in MainActor.assumeIsolated { self.engineLine(line) } },
            onDown: { down in MainActor.assumeIsolated { self.engineWentDown(down) } })
        self.supervisor = supervisor
        supervisor.start()
    }

    // MARK: The engine

    private func engineStarted(_ pid: pid_t) {
        log("engine started: \(config.engine) \(config.engineArguments.joined(separator: " ")), pid \(pid)")
        guard let stateDirectory else { return }
        do {
            try PidFile(app: getpid(), engine: pid).write(to: stateDirectory)
        } catch {
            log("could not write \(stateDirectory.path)/\(PidFile.name): \(error.localizedDescription)")
        }
    }

    private func engineLine(_ line: Data) {
        guard let head = Wire.decode(Wire.Head.self, from: line) else {
            log("the engine wrote a line that is not an event (\(line.count) bytes)")
            return
        }
        if head.isTerminal {
            let code = head.code.map { " \($0): \(head.message ?? "")" } ?? ""
            log("← \(head.event ?? "") re=\(head.re ?? "-")\(code)")
        }
        apply(router.engineEvent(head, line: String(decoding: line, as: UTF8.self)))
    }

    private func engineWentDown(_ down: EngineSupervisor.Down) {
        log("engine down: \(down.message); starting it again in \(Int(down.retryIn)) s")
        engineDown = down.reason
        helloLine = nil
        watch?.settled(dropped: true)
        let outputs = router.engineExited(message: down.message)
        if case .page(let line)? = outputs.last {
            downLine = line
        }
        apply(outputs)
    }

    private func apply(_ outputs: [Router.Output]) {
        for output in outputs {
            switch output {
            case .engine(let line):
                let request = Wire.decode(Wire.Request.self, from: line)
                // Quit has begun: what was waiting stays unsent, rather than
                // start in an engine that is about to be stopped.
                guard !stopping else {
                    log("not sent, quitting: \(request?.type ?? "a request") id=\(request?.id ?? "-")")
                    continue
                }
                if let request {
                    log("→ \(request.type) id=\(request.id)")
                }
                supervisor?.send(line)
            case .page(let line):
                window?.deliver(line)
            case .shell(let line):
                shellEvent(line)
            case .command(let name):
                pageCommand(name)
            case .copy(let text):
                copy(text)
            }
        }
    }

    private func shellEvent(_ line: String) {
        guard let head = Wire.decode(Wire.Head.self, from: line) else { return }
        // The router's tick after a run is a tick the watch did not send.
        let ofRunTick = head.re?.hasPrefix(Router.runTickPrefix) ?? false
        let ofTick = ofRunTick || head.re?.hasPrefix(Self.tickPrefix) ?? false
        switch head.event {
        case "hello":
            guard let hello = Wire.decode(Wire.Hello.self, from: line) else { return }
            self.hello = hello
            helloLine = line
            engineDown = nil
            downLine = nil
            log("hello: protocol \(hello.protocol), engine \(hello.version), fda \(hello.fda)")
            startWatch()
            readSettings()
        case "tick":
            guard let tick = Wire.decode(Wire.Tick.self, from: line) else { return }
            self.tick = tick
            checkedAt = Date()
            tickLine = line
            log("tick: \(humanBytes(tick.free)) free, \(tick.alerts?.count ?? 0) alerts")
            for notice in AlertText.notices(for: tick) {
                notify(notice)
            }
            window?.deliver(line)
        case "done" where ofTick:
            tickFailure = nil
            lockNoticed = false
            if !ofRunTick {
                watch?.settled()
            }
        case "error" where ofTick:
            tickFailure = TickFailure(code: head.code, message: head.message)
            // A locked gate fails every tick while headroom stays low:
            // it is news once, not every quarter of an hour.
            if let notice = AlertText.notice(forError: head.code ?? "", message: head.message ?? "") {
                if !lockNoticed {
                    notify(notice)
                }
                lockNoticed = true
            } else {
                lockNoticed = false
            }
            if !ofRunTick {
                watch?.settled()
            }
        default:
            break
        }
    }

    /// A copy outside `~/Applications` shares the installed app's bundle
    /// id, and with it the permission to notify: it only logs.
    private func notify(_ notice: Notice) {
        guard Config.installed else {
            log("not ~/Applications/Regrow.app, so no notification: \(notice.title)")
            return
        }
        notifier.post(notice)
    }

    private func startWatch() {
        if let watch {
            watch.resume()
            return
        }
        let watch = Watch(
            queue: .main,
            autotrim: { MainActor.assumeIsolated { self.autotrim } },
            send: { id, autotrim in
                MainActor.assumeIsolated {
                    self.apply(self.router.shellRequest(["type": "tick", "id": id, "autotrim": autotrim]))
                }
            })
        self.watch = watch
        watch.start()
    }

    func woke() {
        log("woke")
        watch?.resume()
    }

    // MARK: The page

    /// The owner opened the app while it runs.
    func reopened() {
        log("opened again: showing the window")
        showWindow(scan: false)
    }

    /// Shows the window; with `scan`, the page also starts a scan.
    func showWindow(scan: Bool) {
        if scan {
            log("scan asked for")
        }
        if let window {
            window.show()
            if scan {
                askScan()
            }
            return
        }
        guard FileManager.default.fileExists(atPath: config.page.path) else {
            log("no scan page at \(config.page.path)")
            let alert = NSAlert()
            alert.messageText = "The scan page is missing"
            alert.informativeText = "This copy of regrow has no page at \(config.page.path)."
            NSApp.activate(ignoringOtherApps: true)
            alert.runModal()
            return
        }
        let window = ScanWindow(
            page: config.page, log: log,
            onMessage: { [weak self] body in
                guard let self else { return }
                self.apply(self.router.pageMessage(body))
            },
            onPageGone: { [weak self] reloading in
                guard let self else { return }
                self.pageReady = false
                self.pageReloaded = reloading
                self.reloadedMidRun = reloading && self.router.executeInFlight
                self.apply(self.router.pageClosed())
            },
            onClose: { [weak self] in
                guard let self else { return }
                self.log("scan window closed")
                // Released after AppKit has finished closing it.
                let closing = self.window
                self.window = nil
                DispatchQueue.main.async { _ = closing }
            },
            onKey: { [weak self] in
                self?.readSettings()
            })
        self.window = window
        pageReady = false
        pageReloaded = false
        reloadedMidRun = false
        scanAsked = scan
        window.show()
        log("scan window opened, page \(config.page.path)")
    }

    private func askScan() {
        if pageReady {
            window?.deliver(Router.shellEvent("scan"))
        } else {
            scanAsked = true
        }
    }

    private func pageCommand(_ name: String) {
        switch name {
        case "ready":
            log("page ready")
            var standing = [helloLine, tickLine, downLine, settings?.line].compactMap { $0 }
            standing.append(update.line(running: runningVersion))
            if let environmentFailure {
                standing.append(environmentLine(environmentFailure))
            }
            standing.forEach { window?.deliver($0) }
            if pageReloaded {
                pageReloaded = false
                window?.deliver(Router.shellEvent("reloaded", ["running": reloadedMidRun]))
            }
            pageReady = true
            readSettings()
            if scanAsked {
                scanAsked = false
                window?.deliver(Router.shellEvent("scan"))
            }
        case "automation":
            // One question at a time: macOS's dialog may still be open, and
            // its answer reaches whichever page is open then.
            guard !askingFinder else {
                log("still waiting for Finder's answer")
                return
            }
            askingFinder = true
            log("asking Finder whether regrow may control it")
            Automation.askFinder(queue: .main) { allowed in
                MainActor.assumeIsolated {
                    self.askingFinder = false
                    self.log("automation \(allowed ? "allowed" : "denied")")
                    self.window?.deliver(Router.shellEvent("automation", ["allowed": allowed]))
                    // Finder can refuse a question with the grant given, as
                    // when it times out: the page then hears the grant again.
                    self.settings = nil
                    self.readSettings()
                }
            }
        case "open-full-disk-access":
            openFullDiskAccess()
        case "login-item-on":
            chooseLoginItem(.register)
        case "login-item-off":
            chooseLoginItem(.unregister)
        case "login-item-settings":
            chooseLoginItem(.openSettings)
        case "notification-settings":
            openNotificationSettings()
        case "automation-settings":
            log("opening Automation settings")
            if let url = URL(string: "x-apple.systempreferences:com.apple.preference.security?Privacy_Automation") {
                NSWorkspace.shared.open(url)
            }
        case "show-log":
            NSWorkspace.shared.activateFileViewerSelecting([config.log])
        case "show-staging":
            showStaging()
        case "relaunch":
            relaunch()
        case "app-update-check":
            checkUpdate()
        case "app-update-install":
            installUpdate()
        default:
            log("the page sent a shell message this app does not know: \(name)")
        }
    }

    private func environmentLine(_ reason: String) -> String {
        Router.shellEvent("environment", ["captured": false, "reason": reason])
    }

    // MARK: What the menu does

    func openFullDiskAccess() {
        log("opening Full Disk Access settings")
        if let url = URL(string: "x-apple.systempreferences:com.apple.preference.security?Privacy_AllFiles") {
            NSWorkspace.shared.open(url)
        }
    }

    private func openNotificationSettings() {
        log("opening Notifications settings")
        let id = Bundle.main.bundleIdentifier.map { "?id=\($0)" } ?? ""
        if let url = URL(string: "x-apple.systempreferences:com.apple.Notifications-Settings.extension\(id)") {
            NSWorkspace.shared.open(url)
        }
    }

    func copy(_ text: String) {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(text, forType: .string)
    }

    func showStaging() {
        guard let stateDirectory else { return }
        NSWorkspace.shared.activateFileViewerSelecting([stateDirectory.appendingPathComponent("staging")])
    }

    /// Quits, and has a helper open the bundle again once this process
    /// has gone. The helper gives up waiting after a minute.
    func relaunch() {
        guard !stopping, !handedOff else {
            log("relaunch asked for while the app quits")
            return
        }
        let bundle = Bundle.main.bundlePath
        guard bundle.hasSuffix(".app") else {
            log("relaunch asked for outside a bundle: quit and start it again by hand")
            return
        }
        let wait = #"i=0; while kill -0 "$1" 2>/dev/null && [ "$i" -lt 600 ]; do sleep 0.1; i=$((i+1)); done; exec /usr/bin/open "$2" --args --open-scan"#
        do {
            try quit(handingOff: wait, arguments: [bundle])
            log("relaunching")
        } catch {
            log("could not relaunch: \(error)")
        }
    }

    /// Starts `script` in a session of its own, this process's pid its
    /// first argument, and quits.
    private func quit(handingOff script: String, arguments: [String]) throws {
        let null = open("/dev/null", O_RDWR | O_CLOEXEC)
        defer { close(null) }
        _ = try Spawn.start(
            "/bin/sh", arguments: ["-c", script, "sh", "\(getpid())"] + arguments,
            environment: Account.environment(),
            stdin: null, stdout: null, stderr: null, group: .session)
        handedOff = true
        MainRunLoop.quit()
    }

    // MARK: The app's own update

    private var runningVersion: String {
        Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "unknown"
    }

    private func setUpdate(_ phase: AppUpdate.Phase) {
        update = phase
        window?.deliver(phase.line(running: runningVersion))
    }

    /// Why this copy does not update itself, or nil when it does.
    private var updateRefusal: String? {
        do {
            _ = try AppUpdate.installed(running: Bundle.main.bundleURL)
        } catch {
            return "\(error)"
        }
        // An ad hoc signature holds no grant across builds, so no release can keep one for it.
        return CodeSignature.runningSealed ? nil : "this copy is a development build, signed ad hoc"
    }

    private func checkUpdate() {
        switch update {
        case .checking, .installing: return
        default: break
        }
        if let refusal = updateRefusal {
            setUpdate(.unavailable(refusal))
            return
        }
        setUpdate(.checking)
        let running = runningVersion
        let fetch = AppUpdate.fetch(userAgent: "Regrow/\(running)")
        Task {
            do {
                let release = try await AppUpdate.latest(fetch: fetch)
                switch try AppUpdate.standing(of: release, running: running) {
                case .latest:
                    updateRelease = nil
                    setUpdate(.latest)
                case .newer:
                    updateRelease = release
                    setUpdate(.newer(release.version.description))
                case .older:
                    updateRelease = nil
                    setUpdate(.older(release.version.description))
                }
            } catch {
                log("update check: \(error)")
                setUpdate(.failed(AppUpdate.reason(error)))
            }
        }
    }

    /// Downloads and checks the newer release's app, then quits and has a
    /// helper put it in place and open it.
    private func installUpdate() {
        guard case .newer = update, let release = updateRelease, !stopping, !handedOff else { return }
        guard !router.executeInFlight else {
            setUpdate(.failed("a run is going; update once it has ended"))
            return
        }
        let requirement: SecRequirement
        do {
            requirement = try AppUpdate.runningRequirement()
        } catch {
            setUpdate(.failed(AppUpdate.reason(error)))
            return
        }
        setUpdate(.installing(release.version.description))
        log("updating to \(release.tag)")
        let staged = AppUpdate.stagedPath()
        let fetch = AppUpdate.fetch(userAgent: "Regrow/\(runningVersion)")
        Task {
            var handed = false
            defer { if !handed { try? AppUpdate.discard(staged) } }
            do {
                try await AppUpdate.stage(release, requirement: requirement, staged: staged, fetch: fetch)
            } catch {
                log("update: \(error)")
                setUpdate(.failed(AppUpdate.reason(error)))
                return
            }
            // Quit, or Relaunch, was chosen during the download.
            guard !stopping, !handedOff else {
                log("update: the app quit before it was put in place")
                return
            }
            guard !router.executeInFlight else {
                setUpdate(.failed("a run started; update once it has ended"))
                return
            }
            handed = relaunch(swapping: staged)
        }
    }

    /// Quits, and has a helper put the staged bundle in place once this
    /// process has gone, then open the app: the new one, or this one
    /// again when the swap fails. A process still there after 90 s keeps
    /// its bundle, and the helper gives up. Either way the helper empties
    /// the staged path, and writes a failure to the app's log. Returns
    /// whether the helper was started.
    private func relaunch(swapping staged: URL) -> Bool {
        let destination: URL
        do {
            destination = try AppUpdate.installed(running: Bundle.main.bundleURL)
        } catch {
            setUpdate(.failed(AppUpdate.reason(error)))
            return false
        }
        // A command whose redirection fails is not run, so nothing but `say` writes to the log.
        let script = #"""
            log=$4
            { : >>"$log"; } 2>/dev/null || log=/dev/null
            say() { printf '%s update helper: %s\n' "$(/bin/date +%Y-%m-%dT%H:%M:%S%z | /usr/bin/sed 's/\(..\)$/:\1/')" "$1" >>"$log"; }
            i=0; while kill -0 "$1" 2>/dev/null && [ "$i" -lt 900 ]; do sleep 0.1; i=$((i+1)); done
            if kill -0 "$1" 2>/dev/null; then
                say "gave up: Regrow, pid $1, was still running after 90 s"
                rm -rf "$2"
                exit 1
            fi
            if ! out=$("$2/Contents/MacOS/Regrow" --swap "$2" "$3" 2>&1); then
                say "the swap failed, so the old app opens again: $(printf '%s' "$out" | /usr/bin/tr '\n' ' ')"
            fi
            rm -rf "$2" || say "could not remove $2"
            /usr/bin/open "$3" || say "could not open $3"
            """#
        do {
            try quit(handingOff: script, arguments: [staged.path, destination.path, config.log.path])
            log("quitting for the update")
            return true
        } catch {
            log("could not start the update's helper: \(error)")
            setUpdate(.failed(AppUpdate.reason(error)))
            return false
        }
    }

    // MARK: Quit

    /// Runs the stop ladder on the engine, then calls `done` on the
    /// main thread.
    func shutdown(_ done: @escaping @MainActor () -> Void) {
        guard !stopping else { return }
        stopping = true
        watch?.stop()
        let executing = router.executeInFlight
        log(executing ? "quitting while an execute is in flight" : "quitting")

        let supervisor = self.supervisor
        let state = stateDirectory
        let log = self.log
        DispatchQueue.global(qos: .userInitiated).async {
            if let rung = supervisor?.stop(executeInFlight: executing) {
                log("engine stopped by \(rung.rawValue)")
            }
            // An engine that is somehow still there keeps its pid file,
            // so the next launch finds it.
            if let state, supervisor?.isRunning != true, PidFile.read(from: state)?.app == getpid() {
                PidFile.remove(from: state)
            }
            log("quit")
            MainRunLoop.perform(done)
        }
    }
}
