import Foundation

/// Puts a staged bundle at its destination in one step, so a reader of
/// the destination sees the old bundle or the new one, never a mix.
public enum BundleSwap {
    public struct Failure: Error, CustomStringConvertible {
        public let description: String
    }

    /// A bundle puts only itself in place, and only at
    /// `~/Applications/Regrow.app`: run through LaunchServices it holds
    /// the owner's grants, and a swap of two other paths would move a
    /// protected folder where anything can read it. Returns the two
    /// paths to swap, as resolved here: a link the caller named is
    /// never what moves.
    public static func check(
        staged: URL, destination: URL, running: URL, home: URL = Account.home
    ) throws -> (staged: URL, destination: URL) {
        func resolved(_ url: URL) -> URL { url.standardizedFileURL.resolvingSymlinksInPath() }
        let bundle = resolved(running), installed = home.appendingPathComponent("Applications/Regrow.app")
        guard resolved(staged).path == bundle.path else {
            throw Failure(description: "\(staged.path) is not this bundle (\(running.path))")
        }
        guard resolved(destination).path == resolved(installed).path else {
            throw Failure(description: "\(destination.path) is not ~/Applications/Regrow.app")
        }
        return (bundle, installed)
    }

    /// With a destination in place the two trade names, and the old
    /// bundle is left at `staged`. Without one, `staged` is renamed.
    public static func swap(staged: URL, destination: URL) throws {
        var info = stat()
        let exists = lstat(destination.path, &info) == 0
        let result =
            exists
            ? renamex_np(staged.path, destination.path, UInt32(RENAME_SWAP))
            : rename(staged.path, destination.path)
        guard result == 0 else {
            let reason = String(cString: strerror(errno))
            let verb = exists ? "swap" : "rename"
            throw Failure(description: "\(verb) \(staged.path) → \(destination.path): \(reason)")
        }
    }
}
