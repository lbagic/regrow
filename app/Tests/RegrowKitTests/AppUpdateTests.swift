import CryptoKit
import Foundation
import RegrowKit
import Testing

@Suite("App update")
struct AppUpdateTests {
    @Test("a version is three plain numbers, compared as numbers")
    func versions() throws {
        #expect(AppUpdate.Version("0.2.0")?.description == "0.2.0")
        for text in ["0.2", "0.2.0.1", "0.2.0-rc1", "v0.2.0", "0..1", "", "a.b.c", "1.2.-3"] {
            #expect(AppUpdate.Version(text) == nil, "\(text)")
        }
        let older = try #require(AppUpdate.Version("1.9.0"))
        let newer = try #require(AppUpdate.Version("1.10.0"))
        #expect(older < newer)
    }

    @Test("GitHub's record gives the tag's version and each asset's URL")
    func record() throws {
        let release = try AppUpdate.Release.decode(releaseRecord(tag: "v0.3.0", assets: ["Regrow.app.zip", "checksums.txt"]))
        #expect(release.tag == "v0.3.0")
        #expect(release.version.description == "0.3.0")
        #expect(release.assets["Regrow.app.zip"] == URL(string: "https://example.invalid/Regrow.app.zip"))
        for tag in ["0.3.0", "v0.3.0-rc1", "v0.3"] {
            #expect(throws: AppUpdate.Failure.self) { try AppUpdate.Release.decode(releaseRecord(tag: tag, assets: [])) }
        }
        #expect(throws: AppUpdate.Failure.self) { try AppUpdate.Release.decode(Data("{}".utf8)) }
        do {
            _ = try AppUpdate.Release.decode(Data(#"{"tag_name": 3}"#.utf8))
            Issue.record("a record whose tag is a number decodes")
        } catch let failure as AppUpdate.Failure {
            #expect(!failure.description.contains("CodingKeys"), "the row gets words, not the decoder's dump")
        }
    }

    @Test("the latest release is newer, the same or older; a development build updates from none")
    func standing() throws {
        let release = try AppUpdate.Release.decode(releaseRecord(tag: "v0.3.0", assets: []))
        #expect(try AppUpdate.standing(of: release, running: "0.2.9") == .newer)
        #expect(try AppUpdate.standing(of: release, running: "0.3.0") == .latest)
        #expect(try AppUpdate.standing(of: release, running: "0.10.0") == .older)
        #expect(throws: AppUpdate.Failure.self) { try AppUpdate.standing(of: release, running: "0.3.0-dev") }
    }

    @Test("the row's phase reaches the page with the running version, and the release or reason it has")
    func phaseLine() throws {
        func fields(_ phase: AppUpdate.Phase) throws -> [String: String] {
            let line = phase.line(running: "0.2.0")
            return try #require(JSONSerialization.jsonObject(with: Data(line.utf8)) as? [String: String])
        }
        #expect(try fields(.unchecked) == ["event": "shell", "state": "app-update", "phase": "unchecked", "running": "0.2.0"])
        #expect(try fields(.newer("0.3.0"))["release"] == "0.3.0")
        #expect(try fields(.installing("0.3.0"))["phase"] == "installing")
        #expect(try fields(.failed("offline"))["reason"] == "offline")
        #expect(try fields(.unavailable("ad hoc")) == [
            "event": "shell", "state": "app-update", "phase": "unavailable", "running": "0.2.0", "reason": "ad hoc",
        ])
    }

    @Test("a checksums line is found by its name and must be a SHA-256")
    func checksums() throws {
        let sum = String(repeating: "Ab", count: 32)
        let listing = "\(String(repeating: "0", count: 64))  regrow_darwin_arm64.tar.gz\n\(sum)  Regrow.app.zip\n"
        #expect(try AppUpdate.checksum(for: "Regrow.app.zip", in: listing) == sum.lowercased())
        #expect(throws: AppUpdate.Failure.self) { try AppUpdate.checksum(for: "other.zip", in: listing) }
        #expect(throws: AppUpdate.Failure.self) { try AppUpdate.checksum(for: "x", in: "abc  x\n") }
    }

    @Test("only the copy at ~/Applications/Regrow.app updates itself, a link to it included")
    func installedOnly() async throws {
        try await withTemporaryDirectory { home in
            let applications = home.appendingPathComponent("Applications")
            let installed = applications.appendingPathComponent("Regrow.app")
            try FileManager.default.createDirectory(at: installed, withIntermediateDirectories: true)
            #expect(try AppUpdate.installed(running: installed, home: home).path == installed.path)
            let link = home.appendingPathComponent("link.app")
            try FileManager.default.createSymbolicLink(at: link, withDestinationURL: installed)
            #expect(try AppUpdate.installed(running: link, home: home).path == installed.path)
            let elsewhere = home.appendingPathComponent("Regrow.app")
            try FileManager.default.createDirectory(at: elsewhere, withIntermediateDirectories: true)
            #expect(throws: AppUpdate.Failure.self) { try AppUpdate.installed(running: elsewhere, home: home) }
        }
    }

    @Test("a link at ~/Applications/Regrow.app does not update: the swap would replace the link, its target left old")
    func linkedDestination() async throws {
        try await withTemporaryDirectory { home in
            let real = home.appendingPathComponent("System-Applications/Regrow.app")
            try FileManager.default.createDirectory(at: real, withIntermediateDirectories: true)
            let applications = home.appendingPathComponent("Applications")
            try FileManager.default.createDirectory(at: applications, withIntermediateDirectories: true)
            try FileManager.default.createSymbolicLink(
                at: applications.appendingPathComponent("Regrow.app"), withDestinationURL: real)
            #expect(throws: AppUpdate.Failure.self) { try AppUpdate.installed(running: real, home: home) }
        }
    }

    @Test("an app moved or deleted while it ran says so, not that a link is in its place")
    func movedAway() async throws {
        try await withTemporaryDirectory { home in
            let gone = home.appendingPathComponent("Applications/Regrow.app")
            do {
                _ = try AppUpdate.installed(running: gone, home: home)
                Issue.record("a missing bundle updates")
            } catch let failure as AppUpdate.Failure {
                #expect(failure.description.contains("moved or deleted"))
            }
        }
    }

    @Test("the row gives a system error in words, not its domain and code")
    func reasons() {
        let reason = AppUpdate.reason(CocoaError(.fileWriteNoPermission))
        #expect(!reason.isEmpty)
        #expect(!reason.contains("Domain"))
    }

    @Test("a release whose app matches its checksum and the running signature is staged")
    func stages() async throws {
        try await withTemporaryDirectory { directory in
            let made = try makeRelease(in: directory, version: "0.3.0")
            let staged = directory.appendingPathComponent("Applications/.Regrow.app.staged")
            try FileManager.default.createDirectory(at: staged, withIntermediateDirectories: true)
            try Data("left over".utf8).write(to: staged.appendingPathComponent("old"))

            try await AppUpdate.stage(made.release, requirement: made.requirement, staged: staged, fetch: made.fetch)

            #expect(AppUpdate.shortVersion(of: staged) == "0.3.0")
            #expect(!FileManager.default.fileExists(atPath: staged.appendingPathComponent("old").path))
            try AppUpdate.verify(staged, satisfies: made.requirement)
        }
    }

    @Test("an archive that does not match checksums.txt stages nothing, and clears what an earlier attempt staged")
    func checksumMismatch() async throws {
        try await withTemporaryDirectory { directory in
            let made = try makeRelease(in: directory, version: "0.3.0")
            let staged = directory.appendingPathComponent("Applications/.Regrow.app.staged")
            try FileManager.default.createDirectory(at: staged, withIntermediateDirectories: true)
            try Data("left over".utf8).write(to: staged.appendingPathComponent("old"))
            let tampered: AppUpdate.Fetch = { url, limit in
                let data = try await made.fetch(url, limit)
                return url.lastPathComponent == "Regrow.app.zip" ? data + Data([0]) : data
            }
            await #expect(throws: AppUpdate.Failure.self) {
                try await AppUpdate.stage(made.release, requirement: made.requirement, staged: staged, fetch: tampered)
            }
            #expect(!present(staged))
        }
    }

    @Test("an app signed otherwise than the running one stages nothing")
    func otherSignature() async throws {
        try await withTemporaryDirectory { directory in
            let made = try makeRelease(in: directory, version: "0.3.0")
            let other = try makeBundle(in: directory.appendingPathComponent("other"), version: "0.3.0", marker: "other")
            let staged = directory.appendingPathComponent("Applications/.Regrow.app.staged")
            do {
                try await AppUpdate.stage(
                    made.release, requirement: try AppUpdate.requirement(of: other), staged: staged, fetch: made.fetch)
                Issue.record("staged an app signed otherwise")
            } catch let failure as AppUpdate.Failure {
                #expect(failure.description.contains("not signed as this one is"))
            }
            #expect(!FileManager.default.fileExists(atPath: staged.path))
        }
    }

    @Test("an app whose version is not the release's stages nothing")
    func versionMismatch() async throws {
        try await withTemporaryDirectory { directory in
            let made = try makeRelease(in: directory, version: "0.2.0", tag: "v0.3.0")
            let staged = directory.appendingPathComponent("Applications/.Regrow.app.staged")
            await #expect(throws: AppUpdate.Failure.self) {
                try await AppUpdate.stage(made.release, requirement: made.requirement, staged: staged, fetch: made.fetch)
            }
            #expect(!FileManager.default.fileExists(atPath: staged.path))
        }
    }

    @Test("a release without the app or its checksums stages nothing")
    func missingAssets() async throws {
        try await withTemporaryDirectory { directory in
            let made = try makeRelease(in: directory, version: "0.3.0")
            let staged = directory.appendingPathComponent("Applications/.Regrow.app.staged")
            for missing in ["Regrow.app.zip", "checksums.txt"] {
                let names = ["Regrow.app.zip", "checksums.txt"].filter { $0 != missing }
                let release = try AppUpdate.Release.decode(releaseRecord(tag: "v0.3.0", assets: names))
                await #expect(throws: AppUpdate.Failure.self) {
                    try await AppUpdate.stage(release, requirement: made.requirement, staged: staged, fetch: made.fetch)
                }
            }
            #expect(!FileManager.default.fileExists(atPath: staged.path))
        }
    }

    @Test("an archive whose Regrow.app is a link stages nothing, the bundle it points to passing every check")
    func linkedBundle() async throws {
        try await withTemporaryDirectory { directory in
            let outside = try makeBundle(in: directory.appendingPathComponent("outside"), version: "0.3.0", marker: "release")
            let packing = directory.appendingPathComponent("packing")
            try FileManager.default.createDirectory(at: packing, withIntermediateDirectories: true)
            try FileManager.default.createSymbolicLink(
                at: packing.appendingPathComponent("Regrow.app"), withDestinationURL: outside)
            let zip = directory.appendingPathComponent("Regrow.app.zip")
            try run("/bin/sh", ["-c", #"cd "$1" && /usr/bin/zip -y -q "$2" Regrow.app"#, "sh", packing.path, zip.path])
            let made = try serve(zip, tag: "v0.3.0", requirement: try AppUpdate.requirement(of: outside))
            let staged = directory.appendingPathComponent("Applications/.Regrow.app.staged")
            do {
                try await AppUpdate.stage(made.release, requirement: made.requirement, staged: staged, fetch: made.fetch)
                Issue.record("staged a link")
            } catch let failure as AppUpdate.Failure {
                #expect(failure.description.contains("holds no Regrow.app folder"))
            }
            #expect(!present(staged))
        }
    }

    @Test("a change to either architecture of the app's code, or of its engine's, is refused")
    func everySlice() async throws {
        try await withTemporaryDirectory { directory in
            let bundle = try makeBundle(in: directory, version: "0.3.0", marker: "release")
            let requirement = try AppUpdate.requirement(of: bundle)
            try AppUpdate.verify(bundle, satisfies: requirement)
            for executable in ["Contents/MacOS/Regrow", "Contents/MacOS/regrow-engine"] {
                let file = bundle.appendingPathComponent(executable)
                let original = try Data(contentsOf: file)
                let slices = sliceOffsets(original)
                #expect(slices.count == 2, "\(executable) holds two architectures")
                for slice in slices {
                    var changed = original
                    // The 64-bit Mach-O header's reserved word: hashed, and read by nothing else.
                    changed[slice + 28] ^= 1
                    try changed.write(to: file)
                    #expect(throws: AppUpdate.Failure.self, "\(executable), slice at \(slice)") {
                        try AppUpdate.verify(bundle, satisfies: requirement)
                    }
                }
                try original.write(to: file)
                try AppUpdate.verify(bundle, satisfies: requirement)
            }
        }
    }
}

private func releaseRecord(tag: String, assets: [String]) -> Data {
    let list = assets.map { ["name": $0, "browser_download_url": "https://example.invalid/\($0)"] }
    return try! JSONSerialization.data(withJSONObject: ["tag_name": tag, "assets": list])
}

private struct MadeRelease {
    let release: AppUpdate.Release
    let requirement: SecRequirement
    let fetch: AppUpdate.Fetch
}

/// A release of an ad hoc signed Regrow.app, zipped as the release
/// workflow zips it, served from memory; the requirement is that bundle's own.
private func makeRelease(in directory: URL, version: String, tag: String? = nil) throws -> MadeRelease {
    let bundle = try makeBundle(in: directory.appendingPathComponent("build"), version: version, marker: "release")
    let zip = directory.appendingPathComponent("Regrow.app.zip")
    try run(
        "/usr/bin/ditto",
        ["-c", "-k", "--norsrc", "--noextattr", "--noqtn", "--noacl", "--keepParent", bundle.path, zip.path])
    return try serve(zip, tag: tag ?? "v\(version)", requirement: try AppUpdate.requirement(of: bundle))
}

/// A release of `zip`, served from memory, with a checksums.txt that lists it.
private func serve(_ zip: URL, tag: String, requirement: SecRequirement) throws -> MadeRelease {
    let archive = try Data(contentsOf: zip)
    let sum = SHA256.hash(data: archive).map { String(format: "%02x", $0) }.joined()
    let sums = Data("\(sum)  Regrow.app.zip\n".utf8)
    let release = try AppUpdate.Release.decode(releaseRecord(tag: tag, assets: ["Regrow.app.zip", "checksums.txt"]))
    let served = ["Regrow.app.zip": archive, "checksums.txt": sums]
    return MadeRelease(release: release, requirement: requirement) { url, _ in
        guard let data = served[url.lastPathComponent] else { throw URLError(.fileDoesNotExist) }
        return data
    }
}

/// A minimal app bundle, signed ad hoc, with a nested engine as a
/// release has; both executables hold two architectures, and `marker`
/// makes the bundle's code differ.
private func makeBundle(in directory: URL, version: String, marker: String) throws -> URL {
    let bundle = directory.appendingPathComponent("Regrow.app")
    let macOS = bundle.appendingPathComponent("Contents/MacOS")
    try FileManager.default.createDirectory(at: macOS, withIntermediateDirectories: true)
    let info: [String: Any] = [
        "CFBundleExecutable": "Regrow", "CFBundleIdentifier": "com.github.lbagic.regrow",
        "CFBundlePackageType": "APPL", "CFBundleShortVersionString": version,
    ]
    try PropertyListSerialization.data(fromPropertyList: info, format: .xml, options: 0)
        .write(to: bundle.appendingPathComponent("Contents/Info.plist"))
    let executable = macOS.appendingPathComponent("Regrow")
    try FileManager.default.copyItem(atPath: "/usr/bin/true", toPath: executable.path)
    let resources = bundle.appendingPathComponent("Contents/Resources")
    try FileManager.default.createDirectory(at: resources, withIntermediateDirectories: true)
    try Data(marker.utf8).write(to: resources.appendingPathComponent("marker"))
    // The engine first: the bundle's signature seals it.
    let engine = macOS.appendingPathComponent("regrow-engine")
    try FileManager.default.copyItem(atPath: "/bin/echo", toPath: engine.path)
    try run("/usr/bin/codesign", ["--force", "--sign", "-", engine.path])
    try run("/usr/bin/codesign", ["--force", "--sign", "-", bundle.path])
    return bundle
}

/// Where each architecture's code starts in a universal binary's file.
private func sliceOffsets(_ data: Data) -> [Int] {
    func word(_ at: Int) -> Int { data[at..<at + 4].reduce(0) { $0 << 8 | Int($1) } }
    guard word(0) == 0xcafe_babe else { return [0] }
    return (0..<word(4)).map { word(8 + $0 * 20 + 8) }
}

/// Something is at `url`, a link included.
private func present(_ url: URL) -> Bool {
    (try? FileManager.default.attributesOfItem(atPath: url.path)) != nil
}

private func run(_ executable: String, _ arguments: [String]) throws {
    let process = Process()
    process.executableURL = URL(fileURLWithPath: executable)
    process.arguments = arguments
    process.standardOutput = FileHandle.nullDevice
    process.standardError = FileHandle.nullDevice
    try process.run()
    process.waitUntilExit()
    guard process.terminationStatus == 0 else {
        throw CocoaError(.executableLoad, userInfo: [NSLocalizedDescriptionKey: "\(executable) exited \(process.terminationStatus)"])
    }
}
