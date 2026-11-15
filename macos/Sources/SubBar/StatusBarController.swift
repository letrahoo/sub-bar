import AppKit
import SwiftUI

@MainActor
final class StatusBarController: NSObject {
    private let statusItem: NSStatusItem
    private let popover: NSPopover
    private let appState: AppState

    init(appState: AppState) {
        self.appState = appState
        self.statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        self.popover = NSPopover()
        super.init()

        popover.behavior = .transient
        popover.animates = true
        popover.contentSize = NSSize(width: 400, height: 640)
        popover.contentViewController = NSHostingController(rootView: PopoverView(state: appState))

        if let button = statusItem.button {
            button.target = self
            button.action = #selector(handleStatusBarClick(_:))
            button.sendAction(on: [.leftMouseUp, .rightMouseUp])
        }

        appState.onMenuBarNeedsUpdate = { [weak self] in
            self?.renderStatusItem()
        }
        renderStatusItem()
    }

    @objc private func handleStatusBarClick(_ sender: NSStatusBarButton) {
        guard let event = NSApp.currentEvent else {
            togglePopover(sender)
            return
        }
        if event.type == .rightMouseUp || event.modifierFlags.contains(.option) {
            showContextMenu(sender)
        } else {
            togglePopover(sender)
        }
    }

    private func togglePopover(_ sender: NSStatusBarButton) {
        if popover.isShown {
            popover.performClose(sender)
        } else {
            showPopover()
        }
    }

    func showPopover() {
        guard let button = statusItem.button else { return }
        Task { await appState.loadAll() }
        if !popover.isShown {
            popover.show(relativeTo: button.bounds, of: button, preferredEdge: .minY)
        }
        NSApp.activate(ignoringOtherApps: true)
        popover.contentViewController?.view.window?.makeKey()
    }

    private func showContextMenu(_ sender: NSStatusBarButton) {
        let menu = NSMenu()
        let refreshItem = NSMenuItem(title: "立即刷新全部入口", action: #selector(menuRefreshAll), keyEquivalent: "r")
        refreshItem.target = self
        menu.addItem(refreshItem)

        let webItem = NSMenuItem(title: "打开 Web 管理台…", action: #selector(menuOpenWeb), keyEquivalent: "o")
        webItem.target = self
        menu.addItem(webItem)

        menu.addItem(.separator())

        let quitItem = NSMenuItem(title: "退出 SubBar", action: #selector(menuQuit), keyEquivalent: "q")
        quitItem.target = self
        menu.addItem(quitItem)

        statusItem.menu = menu
        sender.performClick(nil)
        statusItem.menu = nil
    }

    @objc private func menuRefreshAll() {
        Task { await appState.triggerRefreshAll() }
    }

    @objc private func menuOpenWeb() {
        if let url = appState.webDashboardURL() {
            NSWorkspace.shared.open(url)
        }
    }

    @objc private func menuQuit() {
        NSApplication.shared.terminate(nil)
    }

    // MARK: - Dynamic Menu Bar Rendering (CodexBar-inspired lightweight modes)

    func renderStatusItem() {
        guard let button = statusItem.button else { return }

        // Only display entries that have already obtained quota or balance;
        // initial-error entries (never had quota/balance) are excluded, while entries
        // that become abnormal AFTER having quota/balance stay visible on the menu bar.
        let activeEntries = (appState.snapshot?.entries ?? []).filter { $0.enabled && $0.hasQuotaOrBalance }
        let pinned = activeEntries.filter { $0.pinned }
        let topTargets = Array((pinned.isEmpty ? activeEntries : pinned).prefix(2))

        let overallSeverity = computeOverallSeverity(activeEntries)

        if appState.errorMessage != nil && appState.snapshot == nil {
            button.image = drawIconWithDot(severity: "error")
            button.imagePosition = .imageLeading
            button.attributedTitle = monoTitle("离线")
            return
        }

        switch appState.menuBarStyle {
        case .iconOnly:
            button.attributedTitle = NSAttributedString(string: "")
            button.image = drawIconWithDot(severity: overallSeverity)
            button.imagePosition = .imageOnly

        case .inline:
            button.image = drawIconWithDot(severity: overallSeverity)
            button.imagePosition = .imageLeading
            if topTargets.isEmpty {
                button.attributedTitle = monoTitle("SubBar")
            } else {
                let text = topTargets.map {
                    "\($0.badge) \($0.barCompactValue(showRemaining: appState.showRemaining))"
                }.joined(separator: " · ")
                button.attributedTitle = monoTitle(text)
            }

        case .gauge:
            let primary = topTargets.first
            button.attributedTitle = NSAttributedString(string: "")
            button.image = drawGaugeCapsuleImage(entry: primary, showRemaining: appState.showRemaining, severity: overallSeverity)
            button.imagePosition = .imageOnly

        case .stacked:
            if topTargets.isEmpty {
                button.image = drawIconWithDot(severity: overallSeverity)
                button.imagePosition = .imageLeading
                button.attributedTitle = monoTitle("SubBar")
            } else {
                button.attributedTitle = NSAttributedString(string: "")
                button.image = drawStackedCodexBarImage(targets: topTargets, showRemaining: appState.showRemaining, severity: overallSeverity)
                button.imagePosition = .imageOnly
            }
        }
    }

    private func computeOverallSeverity(_ entries: [EntrySnapshot]) -> String {
        if entries.contains(where: { $0.status == "critical" || $0.status == "error" }) {
            return "critical"
        }
        if entries.contains(where: { $0.status == "warning" || $0.status == "stale" }) {
            return "warning"
        }
        return entries.isEmpty ? "idle" : "ok"
    }

    private func nsStatusColor(_ severity: String) -> NSColor {
        switch severity {
        case "critical", "error": return NSColor(srgbRed: 0.98, green: 0.36, blue: 0.36, alpha: 1.0)
        case "warning", "stale": return NSColor(srgbRed: 0.98, green: 0.75, blue: 0.14, alpha: 1.0)
        case "ok": return NSColor(srgbRed: 0.18, green: 0.83, blue: 0.45, alpha: 1.0)
        default: return NSColor.secondaryLabelColor
        }
    }

    private func monoTitle(_ str: String) -> NSAttributedString {
        let font = NSFont.monospacedDigitSystemFont(ofSize: 12.0, weight: .bold)
        return NSAttributedString(string: " " + str, attributes: [
            .font: font,
            .foregroundColor: NSColor.labelColor
        ])
    }

    private func drawIconWithDot(severity: String) -> NSImage {
        let size = NSSize(width: 20, height: 18)
        let img = NSImage(size: size, flipped: false) { _ in
            let fg = NSColor.labelColor
            fg.setStroke()
            fg.setFill()

            // Crisp stacked-bars glyph
            let bars: [(CGFloat, CGFloat)] = [(3, 13), (8, 9), (13, 11)]
            for (y, w) in bars {
                let path = NSBezierPath(roundedRect: NSRect(x: 2, y: y, width: w, height: 2.5), xRadius: 1.2, yRadius: 1.2)
                path.fill()
            }

            // Status dot bottom-right
            let dotColor = self.nsStatusColor(severity)
            dotColor.setFill()
            let dot = NSBezierPath(ovalIn: NSRect(x: 13.5, y: 2.0, width: 5.0, height: 5.0))
            dot.fill()
            return true
        }
        img.isTemplate = false
        return img
    }

    // Stacked 2-line Menu Bar layout inspired by CodexBar
    private func drawStackedCodexBarImage(targets: [EntrySnapshot], showRemaining: Bool, severity: String) -> NSImage {
        if targets.isEmpty {
            return drawIconWithDot(severity: severity)
        }
        let line1 = "\(targets[0].badge) \(targets[0].barCompactValue(showRemaining: showRemaining))"
        let line2 = targets.count > 1
            ? "\(targets[1].badge) \(targets[1].barCompactValue(showRemaining: showRemaining))"
            : nil

        let font = NSFont.monospacedDigitSystemFont(ofSize: line2 == nil ? 11.5 : 9.5, weight: .bold)
        let attrs: [NSAttributedString.Key: Any] = [
            .font: font,
            .foregroundColor: NSColor.labelColor
        ]
        let s1 = (line1 as NSString).size(withAttributes: attrs)
        let s2 = line2.map { ($0 as NSString).size(withAttributes: attrs) } ?? .zero
        let textW = ceil(max(s1.width, s2.width))
        let totalW = 14 + textW + 4
        let height: CGFloat = 20

        let img = NSImage(size: NSSize(width: totalW, height: height), flipped: false) { _ in
            // Left vertical status pill
            let pillColor = self.nsStatusColor(severity)
            pillColor.setFill()
            let pill = NSBezierPath(roundedRect: NSRect(x: 2, y: 3, width: 4.0, height: 14), xRadius: 2, yRadius: 2)
            pill.fill()

            if let line2 {
                (line1 as NSString).draw(at: NSPoint(x: 9, y: 9.5), withAttributes: attrs)
                (line2 as NSString).draw(at: NSPoint(x: 9, y: 0.5), withAttributes: attrs)
            } else {
                (line1 as NSString).draw(at: NSPoint(x: 9, y: 3.0), withAttributes: attrs)
            }
            return true
        }
        img.isTemplate = false
        return img
    }

    // Mini high-contrast progress bar + label in Menu Bar
    private func drawGaugeCapsuleImage(entry: EntrySnapshot?, showRemaining: Bool, severity: String) -> NSImage {
        guard let entry else { return drawIconWithDot(severity: severity) }
        let label = "\(entry.badge) \(entry.barCompactValue(showRemaining: showRemaining))"
        let font = NSFont.monospacedDigitSystemFont(ofSize: 11.0, weight: .bold)
        let attrs: [NSAttributedString.Key: Any] = [
            .font: font,
            .foregroundColor: NSColor.labelColor
        ]
        let textSize = (label as NSString).size(withAttributes: attrs)
        let barW: CGFloat = 28
        let totalW = barW + 7 + ceil(textSize.width) + 4
        let height: CGFloat = 20

        var fillRatio: CGFloat = 1.0
        if let w = entry.windows?.first(where: { $0.key != "cycle" }) ?? entry.windows?.first {
            let pct = showRemaining ? w.remainingPercent : w.usedPercent
            fillRatio = CGFloat(max(0.06, min(1.0, pct / 100.0)))
        }

        let img = NSImage(size: NSSize(width: totalW, height: height), flipped: false) { _ in
            let trackRect = NSRect(x: 2, y: 5.5, width: barW, height: 9)
            NSColor.labelColor.withAlphaComponent(0.35).setStroke()
            let track = NSBezierPath(roundedRect: trackRect, xRadius: 4.5, yRadius: 4.5)
            track.lineWidth = 1.2
            track.stroke()

            let innerW = max(3, (barW - 2.4) * fillRatio)
            let fillRect = NSRect(x: 3.2, y: 6.7, width: innerW, height: 6.6)
            self.nsStatusColor(entry.status).setFill()
            NSBezierPath(roundedRect: fillRect, xRadius: 3.3, yRadius: 3.3).fill()

            (label as NSString).draw(at: NSPoint(x: barW + 6, y: 3.2), withAttributes: attrs)
            return true
        }
        img.isTemplate = false
        return img
    }
}
