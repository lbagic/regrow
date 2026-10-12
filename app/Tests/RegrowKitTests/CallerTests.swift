import Foundation
import RegrowKit
import Testing

/// What a caller outside the app can choose by how it starts the app:
/// `open --env`, `launchctl setenv` and `open --args` reach a sealed
/// bundle that holds the owner's grants.
@Suite("Caller")
struct CallerTests {
    private static let overrides = [
        "REGROW_ENGINE": "/tmp/caller/engine",
        "REGROW_PAGE": "/tmp/caller/page.html",
        "REGROW_RULES_DIR": "/tmp/caller/rules",
        "REGROW_HOME": "/tmp/caller/home",
        "REGROW_STATE_HOME": "/tmp/caller/state",
        "REGROW_LOG": "/tmp/caller/app.log",
    ]

    private func config(sealed: Bool) -> Config {
        Config.load(
            environment: Self.overrides, arguments: ["/Bundle/Contents/MacOS/Regrow"],
            executable: URL(fileURLWithPath: "/Bundle/Contents/MacOS/Regrow"),
            resources: URL(fileURLWithPath: "/Bundle/Contents/Resources"),
            home: URL(fileURLWithPath: "/Users/owner"), sealed: sealed)
    }

    @Test("a sealed run reads no REGROW_ variable: engine, page, rules, home, state and log are its own")
    func sealedReadsNoOverride() {
        let config = config(sealed: true)
        #expect(config.engine == "/Bundle/Contents/MacOS/regrow-engine")
        #expect(config.page.path == "/Bundle/Contents/Resources/page.html")
        #expect(config.rulesDir == nil)
        #expect(config.home == nil)
        #expect(config.stateHome == nil)
        #expect(config.log.path == "/Users/owner/Library/Logs/regrow/app.log")
        #expect(config.smokeRules.path == "/Bundle/Contents/Resources/smoke")
        #expect(config.engineArguments == ["engine"])
        #expect(config.engineEnvironment(over: ["HOME": "/Users/owner"]) == ["HOME": "/Users/owner"])
    }

    @Test("an ad hoc run takes each REGROW_ variable in place of its default")
    func adHocTakesOverrides() {
        let config = config(sealed: false)
        #expect(config.engine == "/tmp/caller/engine")
        #expect(config.page.path == "/tmp/caller/page.html")
        #expect(config.engineArguments == ["engine", "--rules-dir", "/tmp/caller/rules"])
        #expect(
            config.engineEnvironment(over: [:]) == ["HOME": "/tmp/caller/home", "XDG_STATE_HOME": "/tmp/caller/state"])
        #expect(config.log.path == "/tmp/caller/app.log")
        #expect(config.smokeRules.path == "/Bundle/Contents/Resources/smoke")
    }

    @Test("the account's environment comes from the password database, whatever this process holds")
    func accountEnvironment() throws {
        setenv("REGROW_TEST_FROM_THE_APP", "1", 1)
        let entry = try #require(getpwuid(getuid())?.pointee)
        let environment = Account.environment()
        #expect(Set(environment.keys) == ["HOME", "USER", "LOGNAME", "SHELL", "PATH", "TMPDIR"])
        #expect(environment["HOME"] == String(cString: entry.pw_dir))
        #expect(environment["USER"] == String(cString: entry.pw_name))
        #expect(environment["PATH"] == "/usr/bin:/bin:/usr/sbin:/sbin")
        #expect(environment["TMPDIR"]?.hasSuffix("/T/") == true)
    }

    @Test("the login shell starts from the account's environment, not the app's own")
    func loginShellStartsClean() async throws {
        try await withTemporaryDirectory { directory in
            setenv("REGROW_TEST_FROM_THE_APP", "1", 1)
            let shell = try writeScript("#!/bin/sh\neval \"$4\"\n", named: "shell", in: directory)

            let environment = try await offThread { LoginEnvironment.capture(shell: shell, timeout: 5) }.get()
            #expect(environment["REGROW_TEST_FROM_THE_APP"] == nil)
            #expect(environment["HOME"] == Account.home.path)
            #expect(environment["PATH"] == "/usr/bin:/bin:/usr/sbin:/sbin")
        }
    }

    // MARK: Signatures

    private func copy(_ binary: String, to directory: URL, signing arguments: [String]) throws -> URL {
        let copy = directory.appendingPathComponent(URL(fileURLWithPath: binary).lastPathComponent)
        try FileManager.default.copyItem(atPath: binary, toPath: copy.path)
        let codesign = Process()
        codesign.executableURL = URL(fileURLWithPath: "/usr/bin/codesign")
        codesign.arguments = arguments + [copy.path]
        codesign.standardError = FileHandle.nullDevice
        try codesign.run()
        codesign.waitUntilExit()
        try #require(codesign.terminationStatus == 0)
        return copy
    }

    @Test("code signed by a certificate is sealed")
    func certificateIsSealed() {
        #expect(CodeSignature.sealed(at: URL(fileURLWithPath: "/bin/ls")))
    }

    @Test("ad hoc code is not sealed")
    func adHocIsNotSealed() async throws {
        try await withTemporaryDirectory { directory in
            let copy = try copy("/usr/bin/true", to: directory, signing: ["--force", "--sign", "-"])
            #expect(!CodeSignature.sealed(at: copy))
        }
    }

    @Test("unsigned code is not sealed")
    func unsignedIsNotSealed() async throws {
        try await withTemporaryDirectory { directory in
            let copy = try copy("/usr/bin/true", to: directory, signing: ["--remove-signature"])
            #expect(!CodeSignature.sealed(at: copy))
        }
    }

    @Test("code that cannot be read counts as sealed")
    func unreadableIsSealed() {
        #expect(CodeSignature.sealed(at: URL(fileURLWithPath: "/nonexistent/regrow-engine")))
    }

    // MARK: Smoke directories

    private func smokeDirectory(in temporary: URL, named name: String = "regrow-smoke.AbC123") throws -> URL {
        let directory = temporary.appendingPathComponent(name)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        return directory
    }

    @Test("a sealed smoke run takes a regrow-smoke.<x> directory made in the temp folder")
    func smokeDirectoryOfTheShape() async throws {
        try await withTemporaryDirectory { temporary in
            let directory = try smokeDirectory(in: temporary)
            let taken = try #require(SmokeRun.directory(directory.path, sealed: true, temporary: temporary))
            let real = try #require(realpath(directory.path, nil))
            defer { free(real) }
            #expect(taken.path == String(cString: real))
        }
    }

    @Test("a sealed smoke run refuses any other directory", arguments: [
        "elsewhere", "name", "dots", "link", "file", "missing",
    ])
    func smokeDirectoryRefused(case name: String) async throws {
        try await withTemporaryDirectory { root in
            let temporary = root.appendingPathComponent("T")
            let elsewhere = root.appendingPathComponent("elsewhere/regrow-smoke.AbC123")
            try FileManager.default.createDirectory(at: elsewhere, withIntermediateDirectories: true)
            try FileManager.default.createDirectory(at: temporary, withIntermediateDirectories: true)
            let argument: String
            switch name {
            case "elsewhere":
                argument = elsewhere.path
            case "name":
                argument = try smokeDirectory(in: temporary, named: "regrow-smoke.").path
            case "dots":
                argument = try smokeDirectory(in: temporary, named: "regrow-smoke.a-b").path
            case "link":
                let link = temporary.appendingPathComponent("regrow-smoke.AbC123")
                try FileManager.default.createSymbolicLink(at: link, withDestinationURL: elsewhere)
                argument = link.path
            case "file":
                let file = temporary.appendingPathComponent("regrow-smoke.AbC123")
                try Data().write(to: file)
                argument = file.path
            default:
                argument = temporary.appendingPathComponent("regrow-smoke.AbC123").path
            }
            #expect(SmokeRun.directory(argument, sealed: true, temporary: temporary) == nil)
        }
    }

    @Test("a temp folder that is itself a link takes no smoke directory")
    func linkedTemporaryRefused() async throws {
        try await withTemporaryDirectory { root in
            let real = root.appendingPathComponent("real")
            let directory = try smokeDirectory(in: real)
            let temporary = root.appendingPathComponent("T")
            try FileManager.default.createSymbolicLink(at: temporary, withDestinationURL: real)
            let argument = temporary.appendingPathComponent(directory.lastPathComponent).path
            #expect(SmokeRun.directory(argument, sealed: true, temporary: temporary) == nil)
        }
    }

    @Test("an ad hoc smoke run takes the directory it is given")
    func adHocSmokeDirectory() {
        #expect(SmokeRun.directory("/tmp/anywhere", sealed: false)?.path == "/tmp/anywhere")
    }

    // MARK: Bundle swap

    @Test("a bundle swaps only itself into ~/Applications/Regrow.app")
    func swapItself() throws {
        let home = URL(fileURLWithPath: "/Users/owner")
        let staged = URL(fileURLWithPath: "/Users/owner/Applications/.Regrow.app.staged")
        let installed = URL(fileURLWithPath: "/Users/owner/Applications/Regrow.app")
        let pair = try BundleSwap.check(staged: staged, destination: installed, running: staged, home: home)
        #expect(pair.staged.path == staged.path && pair.destination.path == installed.path)
        #expect(throws: BundleSwap.Failure.self) {
            _ = try BundleSwap.check(
                staged: URL(fileURLWithPath: "/Users/owner/Library/Messages"), destination: installed,
                running: staged, home: home)
        }
        #expect(throws: BundleSwap.Failure.self) {
            _ = try BundleSwap.check(
                staged: staged, destination: URL(fileURLWithPath: "/Users/owner/Library/Messages"),
                running: staged, home: home)
        }
    }

    @Test("a link the caller names for this bundle is not what moves: the bundle itself is put in place")
    func swapThroughALink() async throws {
        try await withTemporaryDirectory { directory in
            let home = directory.appendingPathComponent("home")
            let applications = home.appendingPathComponent("Applications")
            let running = applications.appendingPathComponent(".Regrow.app.staged")
            let installed = applications.appendingPathComponent("Regrow.app")
            for (bundle, mark) in [(running, "new"), (installed, "installed")] {
                try FileManager.default.createDirectory(at: bundle, withIntermediateDirectories: true)
                try Data(mark.utf8).write(to: bundle.appendingPathComponent("which"))
            }
            let callers = directory.appendingPathComponent("callers")
            try FileManager.default.createDirectory(at: callers, withIntermediateDirectories: true)
            let link = callers.appendingPathComponent("staged")
            try FileManager.default.createSymbolicLink(at: link, withDestinationURL: running)

            let (staged, destination) = try BundleSwap.check(
                staged: link, destination: installed, running: running, home: home)
            try BundleSwap.swap(staged: staged, destination: destination)

            let kind = try FileManager.default.attributesOfItem(atPath: installed.path)[.type] as? FileAttributeType
            #expect(kind == .typeDirectory)
            #expect(try String(contentsOf: installed.appendingPathComponent("which"), encoding: .utf8) == "new")
            #expect(try FileManager.default.destinationOfSymbolicLink(atPath: link.path) == running.path)
        }
    }
}
