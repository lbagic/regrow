import AppKit
import RegrowKit
import SwiftUI

/// What the menu's actions do.
struct MenuActions {
    var open: () -> Void
    var setAutotrim: (Bool) -> Void
    var copy: (String) -> Void
    var openFullDiskAccess: () -> Void
    var relaunch: () -> Void
    var showStaging: () -> Void
    var quit: () -> Void
}

/// The menu's rows as AppKit items. Text that does nothing is a view in a
/// disabled item: the arrow keys pass over it, and AppKit greys a title,
/// never a view.
enum MenuItems {
    static let width: CGFloat = 340
    static let inset: CGFloat = 14

    static func make(_ rows: [MenuRow], _ actions: MenuActions) -> [NSMenuItem] {
        rows.map { row in
            switch row {
            case .figures(let figures):
                return hosted(FiguresView(figures: figures).padding(.top, 6).padding(.bottom, 4))
            case .separator:
                return .separator()
            case .open:
                return ActionItem("Open regrow", actions.open)
            case .autotrim(let on):
                // Its state is a figure, not a checkmark: a checkmark makes
                // AppKit indent every item, out of line with the text rows.
                let title = "Trim the Go build cache when space is low"
                let item = ActionItem(title) { actions.setAutotrim(!on) }
                item.attributedTitle = paired(title, on ? "on" : "off")
                return item
            case .note(let text), .environment(let text):
                return hosted(Text(quoted(text)).font(.system(size: 12)).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true).padding(.vertical, 2))
            case .copy(let command):
                return ActionItem("Copy the command to run in Terminal") { actions.copy(command) }
            case .fullDiskAccess(let state):
                return hosted(HStack {
                    Text("Full Disk Access")
                    Spacer()
                    Text(state).foregroundStyle(.secondary)
                }.font(Font(NSFont.menuFont(ofSize: 0))).padding(.vertical, 3))
            case .openFullDiskAccess:
                return ActionItem("Open Full Disk Access settings", actions.openFullDiskAccess)
            case .relaunch:
                return ActionItem("Relaunch to apply", actions.relaunch)
            case .staging(let bytes):
                let item = ActionItem("Show staging in Finder", actions.showStaging)
                item.attributedTitle = paired("Show staging in Finder", humanBytes(bytes))
                return item
            case .quit:
                return ActionItem("Quit regrow", actions.quit)
            }
        }
    }

    private static func hosted<V: View>(_ view: V) -> NSMenuItem {
        let host = NSHostingView(rootView: view.padding(.horizontal, inset).frame(width: width, alignment: .leading))
        host.frame.size = host.fittingSize
        let item = NSMenuItem()
        item.view = host
        item.isEnabled = false
        return item
    }

    /// The engine's words, what they quote in backticks set in mono.
    private static func quoted(_ text: String) -> AttributedString {
        var out = AttributedString()
        for (i, part) in text.components(separatedBy: "`").enumerated() {
            var piece = AttributedString(part)
            if i % 2 == 1 {
                piece.font = .system(size: 11.5, design: .monospaced)
            }
            out += piece
        }
        return out
    }

    /// An action's title with its figure set against the menu's right edge.
    private static func paired(_ title: String, _ figure: String) -> NSAttributedString {
        let font = NSFont.menuFont(ofSize: 0)
        let paragraph = NSMutableParagraphStyle()
        paragraph.tabStops = [NSTextTab(textAlignment: .right, location: width - 2 * inset)]
        let text = NSMutableAttributedString(string: title + "\t", attributes: [.font: font, .paragraphStyle: paragraph])
        text.append(NSAttributedString(string: figure, attributes: [
            .font: font, .paragraphStyle: paragraph, .foregroundColor: NSColor.secondaryLabelColor,
        ]))
        return text
    }
}

/// A menu item that runs a closure.
final class ActionItem: NSMenuItem {
    private let run: () -> Void

    init(_ title: String, _ run: @escaping () -> Void) {
        self.run = run
        super.init(title: title, action: #selector(fire), keyEquivalent: "")
        target = self
    }

    required init(coder: NSCoder) {
        fatalError("not drawn from a nib")
    }

    @objc private func fire() {
        run()
    }
}

/// Free space large, the disk meter, then the figures in words.
struct FiguresView: View {
    let figures: MenuFigures

    var body: some View {
        VStack(alignment: .leading, spacing: 5) {
            if let free = figures.free, let total = figures.total {
                HStack(alignment: .firstTextBaseline, spacing: 5) {
                    Text(free).font(.system(size: 20, weight: .semibold)).monospacedDigit()
                    Text(total).font(.system(size: 13))
                }
                Meter(figures: figures)
            }
            ForEach([figures.details, figures.forecast].compactMap { $0 }, id: \.self) { line in
                Text(line).font(.system(size: 12))
            }
            Text(figures.checked).font(.system(size: 11)).foregroundStyle(.secondary)
            ForEach(figures.problems, id: \.self) { problem in
                Text(problem).font(.system(size: 12))
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
    }
}

/// The disk as three segments: taken, purgeable, free.
struct Meter: View {
    let figures: MenuFigures

    var body: some View {
        GeometryReader { space in
            let width = figures.meter(width: space.size.width)
            HStack(spacing: MenuFigures.meterGap) {
                Rectangle().fill(Color.primary.opacity(0.55)).frame(width: width.taken)
                Rectangle().fill(Color.primary.opacity(0.25)).frame(width: width.purgeable)
                Rectangle().fill(Color.primary.opacity(0.1)).frame(width: width.free)
            }
        }
        .frame(height: 6)
        .clipShape(RoundedRectangle(cornerRadius: 3))
        .padding(.vertical, 2)
        .accessibilityHidden(true)
    }
}
