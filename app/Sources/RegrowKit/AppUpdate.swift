import CryptoKit
import Foundation
import Security

/// Keeps the installed Regrow.app at its latest GitHub release. Only the
/// bundle at ~/Applications/Regrow.app is replaced, only by a release's
/// Regrow.app.zip whose SHA-256 matches the release's checksums.txt, and
/// only when the new bundle's signature satisfies the running app's
/// designated requirement: macOS keeps the app's grants by that
/// requirement, so a bundle that meets it keeps them.
public enum AppUpdate {
    public static let latestURL = URL(string: "https://api.github.com/repos/lbagic/regrow/releases/latest")!
    public static let archiveName = "Regrow.app.zip"
    static let checksumsName = "checksums.txt"

    static let maxRecord = 1 << 20
    static let maxChecksums = 64 << 10
    static let maxArchive = 256 << 20

    /// Reads at most `limit` bytes from a URL, or fails.
    public typealias Fetch = @Sendable (_ url: URL, _ limit: Int) async throws -> Data

    public struct Failure: Error, CustomStringConvertible, Equatable {
        public let description: String
        init(_ description: String) { self.description = description }
    }

    /// A release's major.minor.patch; a prerelease or build suffix is not a release.
    public struct Version: Comparable, CustomStringConvertible, Sendable {
        let parts: [Int]

        public init?(_ text: String) {
            let fields = text.split(separator: ".", omittingEmptySubsequences: false)
            guard fields.count == 3 else { return nil }
            var parts: [Int] = []
            for field in fields {
                guard !field.isEmpty, field.allSatisfy({ ("0"..."9").contains($0) }), let n = Int(field) else {
                    return nil
                }
                parts.append(n)
            }
            self.parts = parts
        }

        public var description: String { parts.map(String.init).joined(separator: ".") }

        public static func < (a: Version, b: Version) -> Bool { a.parts.lexicographicallyPrecedes(b.parts) }
    }

    public struct Release: Equatable, Sendable {
        public let tag: String
        public let version: Version
        /// Asset name → download URL.
        public let assets: [String: URL]

        /// GitHub's release record, as `releases/latest` gives it.
        public static func decode(_ data: Data) throws -> Release {
            struct Record: Decodable {
                struct Asset: Decodable {
                    let name: String
                    let browser_download_url: URL
                }
                let tag_name: String
                let assets: [Asset]
            }
            let record: Record
            do {
                record = try JSONDecoder().decode(Record.self, from: data)
            } catch {
                throw Failure("the latest release record could not be read: \(fault(error))")
            }
            guard record.tag_name.hasPrefix("v"), let version = Version(String(record.tag_name.dropFirst())) else {
                throw Failure("the latest release's tag \"\(record.tag_name)\" is not a version")
            }
            var assets: [String: URL] = [:]
            for asset in record.assets {
                assets[asset.name] = asset.browser_download_url
            }
            return Release(tag: record.tag_name, version: version, assets: assets)
        }
    }

    /// How the latest release stands to the running app.
    public enum Standing: Equatable, Sendable {
        case latest
        case newer
        case older
    }

    public static func standing(of release: Release, running: String) throws -> Standing {
        guard let current = Version(running) else {
            throw Failure("this Regrow is a development build (\(running)); only a release updates itself")
        }
        if release.version == current { return .latest }
        return release.version > current ? .newer : .older
    }

    /// The installed bundle, when the running app is it: a release may
    /// replace only ~/Applications/Regrow.app, the path `--swap` takes.
    public static func installed(running: URL, home: URL = Account.home) throws -> URL {
        let destination = home.appendingPathComponent("Applications").appendingPathComponent("Regrow.app")
        guard running.standardizedFileURL.resolvingSymlinksInPath().path
            == destination.resolvingSymlinksInPath().path
        else {
            throw Failure("this Regrow runs from \(running.path); only the copy in ~/Applications updates itself")
        }
        // `--swap` renames the entry itself: a link there would be replaced, its target left old.
        switch fileType(destination) {
        case S_IFDIR?:
            return destination
        case nil:
            throw Failure("nothing is at ~/Applications/Regrow.app any more: this Regrow was moved or deleted while it ran")
        case S_IFLNK?:
            throw Failure("~/Applications/Regrow.app is a link to this Regrow; only a bundle there updates itself")
        default:
            throw Failure("~/Applications/Regrow.app is not a folder; only a bundle there updates itself")
        }
    }

    /// What the row says of an error: a failure's own words, else the system's.
    public static func reason(_ error: Error) -> String {
        switch error {
        case let failure as Failure: return failure.description
        case let failure as Spawn.Failure: return failure.description
        default: return error.localizedDescription
        }
    }

    /// The running app's designated requirement.
    public static func runningRequirement() throws -> SecRequirement {
        var running: SecCode?
        var code: SecStaticCode?
        guard SecCodeCopySelf([], &running) == errSecSuccess, let running,
            SecCodeCopyStaticCode(running, [], &code) == errSecSuccess, let code
        else { throw Failure("this Regrow cannot read its own signature") }
        return try designatedRequirement(of: code)
    }

    /// The designated requirement of the bundle at `url`.
    public static func requirement(of url: URL) throws -> SecRequirement {
        var code: SecStaticCode?
        guard SecStaticCodeCreateWithPath(url as CFURL, [], &code) == errSecSuccess, let code else {
            throw Failure("\(url.path) cannot be read as code")
        }
        return try designatedRequirement(of: code)
    }

    private static func designatedRequirement(of code: SecStaticCode) throws -> SecRequirement {
        var requirement: SecRequirement?
        guard SecCodeCopyDesignatedRequirement(code, [], &requirement) == errSecSuccess, let requirement else {
            throw Failure("the signature names no designated requirement")
        }
        return requirement
    }

    /// Fails unless the bundle at `url` is sealed, for every architecture
    /// and with its nested code, and its signature satisfies `requirement`.
    public static func verify(_ url: URL, satisfies requirement: SecRequirement) throws {
        var code: SecStaticCode?
        guard SecStaticCodeCreateWithPath(url as CFURL, [], &code) == errSecSuccess, let code else {
            throw Failure("\(url.lastPathComponent) cannot be read as code")
        }
        let flags = SecCSFlags(
            rawValue: kSecCSCheckAllArchitectures | kSecCSStrictValidate | kSecCSCheckNestedCode)
        let status = SecStaticCodeCheckValidity(code, flags, requirement)
        guard status == errSecSuccess else {
            let reason = SecCopyErrorMessageString(status, nil).map { $0 as String } ?? "error \(status)"
            throw Failure("the downloaded Regrow.app is not signed as this one is (\(reason))")
        }
    }

    /// The SHA-256 a `sha256sum` listing gives for `name`, in lowercase hex.
    public static func checksum(for name: String, in listing: String) throws -> String {
        for line in listing.split(whereSeparator: \.isNewline) {
            let fields = line.split(whereSeparator: \.isWhitespace)
            guard fields.count == 2, fields[1] == name else { continue }
            let sum = fields[0].lowercased()
            guard sum.count == 64, sum.allSatisfy(\.isHexDigit) else {
                throw Failure("\(checksumsName)'s line for \(name) is not a SHA-256")
            }
            return sum
        }
        throw Failure("\(checksumsName) has no line for \(name)")
    }

    static func hex(_ digest: SHA256.Digest) -> String {
        digest.map { String(format: "%02x", $0) }.joined()
    }

    public static func shortVersion(of bundle: URL) -> String? {
        let plist = bundle.appendingPathComponent("Contents").appendingPathComponent("Info.plist")
        guard let data = try? Data(contentsOf: plist),
            let info = try? PropertyListSerialization.propertyList(from: data, format: nil) as? [String: Any]
        else { return nil }
        return info["CFBundleShortVersionString"] as? String
    }

    /// The path `--swap` takes the new bundle from.
    public static func stagedPath(home: URL = Account.home) -> URL {
        home.appendingPathComponent("Applications").appendingPathComponent(".Regrow.app.staged")
    }

    /// Downloads the release's app, checks it, and puts it at `staged`.
    /// Whatever an earlier attempt left there goes first, so any failure
    /// leaves nothing at `staged`.
    public static func stage(
        _ release: Release, requirement: SecRequirement, staged: URL, fetch: Fetch
    ) async throws {
        try discard(staged)
        guard let sumsURL = release.assets[checksumsName] else {
            throw Failure("release \(release.tag) has no \(checksumsName)")
        }
        guard let archiveURL = release.assets[archiveName] else {
            throw Failure("release \(release.tag) has no \(archiveName)")
        }
        let sums = try await fetch(sumsURL, maxChecksums)
        let want = try checksum(for: archiveName, in: String(decoding: sums, as: UTF8.self))
        let archive = try await fetch(archiveURL, maxArchive)
        guard hex(SHA256.hash(data: archive)) == want else {
            throw Failure("\(archiveName) of release \(release.tag) does not match its \(checksumsName)")
        }

        let files = FileManager.default
        let parent = staged.deletingLastPathComponent()
        try files.createDirectory(at: parent, withIntermediateDirectories: true)
        // On the staged path's volume, so the bundle moves there by a rename.
        let work = try files.url(
            for: .itemReplacementDirectory, in: .userDomainMask, appropriateFor: parent, create: true)
        defer { try? files.removeItem(at: work) }
        let zip = work.appendingPathComponent(archiveName)
        try archive.write(to: zip)
        let unpacked = work.appendingPathComponent("unpacked")
        try unpack(zip, into: unpacked)
        let bundle = unpacked.appendingPathComponent("Regrow.app")
        // The checks below follow a link, and the move would stage the link itself.
        guard fileType(bundle) == S_IFDIR else {
            throw Failure("\(archiveName) of release \(release.tag) holds no Regrow.app folder")
        }
        try verify(bundle, satisfies: requirement)
        guard let version = shortVersion(of: bundle), version == release.version.description else {
            throw Failure("the Regrow.app in release \(release.tag) says it is \(shortVersion(of: bundle) ?? "no version")")
        }
        try discard(staged)
        try files.moveItem(at: bundle, to: staged)
    }

    /// Removes what is at the staged path, a link there included.
    public static func discard(_ staged: URL) throws {
        if fileType(staged) != nil {
            try FileManager.default.removeItem(at: staged)
        }
    }

    /// The file type `lstat` gives, a link's own; nil when nothing is there.
    private static func fileType(_ url: URL) -> mode_t? {
        var info = stat()
        return lstat(url.path, &info) == 0 ? info.st_mode & S_IFMT : nil
    }

    /// Which part of GitHub's record did not decode, in words.
    private static func fault(_ error: Error) -> String {
        func path(_ keys: [CodingKey]) -> String {
            keys.map { $0.intValue.map { "#\($0)" } ?? $0.stringValue }.joined(separator: ".")
        }
        switch error as? DecodingError {
        case .keyNotFound(let key, let context)?:
            return "it has no \(path(context.codingPath + [key]))"
        case .typeMismatch(_, let context)?, .valueNotFound(_, let context)?:
            return "its \(path(context.codingPath)) is not what GitHub sends"
        default:
            return "it is not the JSON GitHub sends"
        }
    }

    /// `ditto -x -k`, leaving out the archive's quarantine: macOS refuses
    /// to open a quarantined app whose certificate is not Apple's.
    static func unpack(_ zip: URL, into directory: URL) throws {
        let null = open("/dev/null", O_RDWR | O_CLOEXEC)
        guard null >= 0 else { throw Failure("/dev/null: \(String(cString: strerror(errno)))") }
        defer { close(null) }
        let pid = try Spawn.start(
            "/usr/bin/ditto", arguments: ["-x", "-k", "--noqtn", zip.path, directory.path],
            environment: Account.environment(), stdin: null, stdout: null, stderr: null)
        var status: Int32 = 0
        while waitpid(pid, &status, 0) == -1 && errno == EINTR {}
        guard status == 0 else {
            throw Failure("\(archiveName) could not be unpacked (ditto exited with status \(status))")
        }
    }

    /// Each read ends within ten minutes, however slowly its bytes come.
    static let session: URLSession = {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.timeoutIntervalForResource = 600
        return URLSession(configuration: configuration)
    }()

    /// Reads a URL with URLSession, as GitHub's API asks to be read,
    /// holding no more than `limit` bytes of it.
    public static func fetch(userAgent: String) -> Fetch {
        { url, limit in
            var request = URLRequest(url: url, timeoutInterval: 60)
            request.setValue("application/vnd.github+json", forHTTPHeaderField: "Accept")
            request.setValue(userAgent, forHTTPHeaderField: "User-Agent")
            let tooLarge = Failure("\(url.absoluteString) is larger than \(limit) bytes")
            do {
                let (bytes, response) = try await session.bytes(for: request)
                guard let http = response as? HTTPURLResponse, http.statusCode == 200 else {
                    let code = (response as? HTTPURLResponse)?.statusCode ?? 0
                    throw Failure("\(url.absoluteString) answered \(code)")
                }
                guard response.expectedContentLength <= Int64(limit) else { throw tooLarge }
                var data = Data()
                data.reserveCapacity(max(0, Int(response.expectedContentLength)))
                for try await byte in bytes {
                    guard data.count < limit else { throw tooLarge }
                    data.append(byte)
                }
                return data
            } catch let failure as Failure {
                throw failure
            } catch {
                throw Failure("could not reach \(url.host ?? url.absoluteString): \(error.localizedDescription)")
            }
        }
    }

    /// The latest release, from GitHub's record of it, which never names
    /// a draft or a prerelease.
    public static func latest(fetch: Fetch, from url: URL = latestURL) async throws -> Release {
        try Release.decode(try await fetch(url, maxRecord))
    }
}

extension AppUpdate {
    /// Where the window's Regrow.app row stands.
    public enum Phase: Equatable, Sendable {
        case unchecked
        case checking
        case latest
        case newer(String)
        case older(String)
        /// Downloading and checking the version given.
        case installing(String)
        case failed(String)
        /// This copy does not update itself, for the reason given.
        case unavailable(String)

        /// The shell event that hands the page this phase.
        public func line(running: String) -> String {
            var fields: [String: Any] = ["running": running]
            switch self {
            case .unchecked: fields["phase"] = "unchecked"
            case .checking: fields["phase"] = "checking"
            case .latest: fields["phase"] = "latest"
            case .newer(let version): fields.merge(["phase": "newer", "release": version]) { $1 }
            case .older(let version): fields.merge(["phase": "older", "release": version]) { $1 }
            case .installing(let version): fields.merge(["phase": "installing", "release": version]) { $1 }
            case .failed(let reason): fields.merge(["phase": "failed", "reason": reason]) { $1 }
            case .unavailable(let reason): fields.merge(["phase": "unavailable", "reason": reason]) { $1 }
            }
            return Router.shellEvent("app-update", fields)
        }
    }
}
