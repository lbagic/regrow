import AppKit
import Combine
import RegrowKit

/// The menu bar item and its menu. AppKit draws the menu: SwiftUI's
/// menu bar menu greys out every line that does nothing.
@MainActor
final class StatusMenu: NSObject, NSMenuDelegate {
    private let shell: Shell
    private let item = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
    private let menu = NSMenu()
    private var changes: AnyCancellable?
    private var shown: [MenuRow] = []
    private var isOpen = false

    init(shell: Shell) {
        self.shell = shell
        super.init()
        menu.autoenablesItems = false
        menu.delegate = self
        item.menu = menu
        item.button?.image = NSImage(systemSymbolName: "internaldrive", accessibilityDescription: "regrow")
        item.button?.imagePosition = .imageLeading
        // objectWillChange fires before the change lands.
        changes = shell.objectWillChange.sink { [weak self] _ in
            DispatchQueue.main.async { self?.refresh() }
        }
        refresh()
    }

    private func refresh() {
        item.button?.title = shell.barTitle
        if isOpen {
            fill()
        }
    }

    private func fill() {
        let rows = MenuModel.rows(shell.menuState)
        guard rows != shown || menu.items.isEmpty else { return }
        shown = rows
        menu.removeAllItems()
        let shell = shell
        let actions = MenuActions(
            open: { shell.showWindow(scan: false) },
            scan: { shell.showWindow(scan: true) },
            setAutotrim: { shell.autotrim = $0 },
            chooseLoginItem: { shell.chooseLoginItem($0) },
            setAerials: { shell.setAerials(off: $0) },
            openAerialPane: { shell.openAerialPane(surface: $0) },
            copy: { shell.copy($0) },
            openFullDiskAccess: { shell.openFullDiskAccess() },
            relaunch: { shell.relaunch() },
            showStaging: { shell.showStaging() },
            quit: { MainRunLoop.quit() })
        MenuItems.make(rows, actions).forEach(menu.addItem)
    }

    func menuNeedsUpdate(_ menu: NSMenu) {
        fill()
    }

    func menuWillOpen(_ menu: NSMenu) {
        isOpen = true
    }

    func menuDidClose(_ menu: NSMenu) {
        isOpen = false
    }
}
