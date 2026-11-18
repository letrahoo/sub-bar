import SwiftUI
import AppKit
import Combine

enum PopoverTab: String, CaseIterable {
    case all = "全部"
    case sub = "订阅配额"
    case bal = "API 余额"
    case prefs = "偏好设置"

    func includes(_ item: EntrySnapshot) -> Bool {
        switch self {
        case .all, .prefs: return true
        case .sub: return item.kind == "subscription"
        case .bal: return item.kind == "balance"
        }
    }
}

struct PopoverView: View {
    @ObservedObject var state: AppState
    @State private var activeTab: PopoverTab = .all
    @State private var editingEntryID: String? = nil
    @State private var showEditor = false
    @State private var nowTick = Date()

    private let ticker = Timer.publish(every: 30, on: .main, in: .common).autoconnect()

    var body: some View {
        ZStack(alignment: .bottom) {
            HCTheme.bg.ignoresSafeArea()

            VStack(spacing: 0) {
                headerBar
                if activeTab != .prefs { summaryStrip }
                tabBar
                Divider().background(HCTheme.border)

                if activeTab == .prefs {
                    preferencesPane
                } else {
                    entriesScrollList
                }

                footerBar
            }

            if let toast = state.toastMessage {
                Text(toast)
                    .font(.system(size: 12.5, weight: .medium))
                    .foregroundColor(HCTheme.bg)
                    .padding(.horizontal, 14)
                    .padding(.vertical, 7)
                    .background(Capsule().fill(HCTheme.textPrimary))
                    .padding(.bottom, 42)
                    .transition(.opacity)
            }
        }
        .frame(width: 400, height: 640)
        .tint(HCTheme.accent)
        .preferredColorScheme(.dark)
        .onReceive(ticker) { d in nowTick = d }
        .sheet(isPresented: $showEditor) {
            EntryEditorView(state: state, entryID: editingEntryID) {
                showEditor = false
            }
        }
    }

    // MARK: - Header

    private var headerBar: some View {
        HStack(spacing: 8) {
            HStack(spacing: 7) {
                ZStack {
                    RoundedRectangle(cornerRadius: 7)
                        .fill(HCTheme.accent)
                        .frame(width: 26, height: 26)
                    MaterialIcon.chart.glyph(23)
                        .foregroundStyle(HCTheme.bg)
                }
                VStack(alignment: .leading, spacing: 1) {
                    Text("订阅与余额")
                        .font(.system(size: 14.5, weight: .semibold))
                        .foregroundColor(HCTheme.textPrimary)
                    Text(statusSubtitle)
                        .font(.system(size: 11, weight: .semibold).monospacedDigit())
                        .foregroundColor(HCTheme.textMuted)
                }
            }

            Spacer()

            Button {
                state.showRemaining.toggle()
            } label: {
                Text(state.showRemaining ? "看剩余" : "看已用")
                    .font(.system(size: 11.5, weight: .medium))
                    .foregroundColor(HCTheme.textPrimary)
                    .padding(.horizontal, 8)
                    .padding(.vertical, 5)
                    .background(RoundedRectangle(cornerRadius: 7).fill(HCTheme.cardElevated))
                    .overlay(RoundedRectangle(cornerRadius: 7).stroke(HCTheme.border, lineWidth: 1.2))
            }
            .buttonStyle(.plain)
            .help("切换百分比按剩余展示或按已用展示")

            IconButton(icon: .refresh, title: state.isLoading ? "正在刷新" : "刷新全部", busy: state.isLoading) {
                Task { await state.triggerRefreshAll() }
            }

            Button {
                editingEntryID = nil
                showEditor = true
            } label: {
                MaterialIcon.add.label("添加", size: 14)
                    .font(.system(size: 12, weight: .semibold))
                    .foregroundColor(HCTheme.bg)
                    .padding(.horizontal, 9)
                    .padding(.vertical, 5)
                    .background(RoundedRectangle(cornerRadius: 7).fill(HCTheme.accent))
            }
            .buttonStyle(.plain)
        }
        .padding(.horizontal, 12)
        .padding(.top, 11)
        .padding(.bottom, 8)
    }

    private var statusSubtitle: String {
        if state.errorMessage != nil && state.snapshot == nil {
            return "服务未连接"
        }
        if state.errorMessage != nil { return "连接中断 · 显示上次数据" }
        if let gen = state.snapshot?.generatedAt {
            return "\(TimeFmt.relativeAgo(gen, now: nowTick))更新"
        }
        return "同步中…"
    }

    // MARK: - Summary Strip

    private var summaryStrip: some View {
        let sum = state.snapshot?.summary
        let balStr = formatTotals(sum?.availableBalances)
        let monthStr = formatTotals(sum?.monthlyFixedCosts)

        return HStack(spacing: 8) {
            summaryBox(title: "可用 API 余额", value: sum == nil ? "—" : balStr.isEmpty ? "暂无" : balStr)
            summaryBox(
                title: "固定月订阅",
                value: sum == nil ? "—" : monthStr.isEmpty ? "未设置" : "\(monthStr)/月"
            )
        }
        .padding(.horizontal, 12)
        .padding(.bottom, 8)
    }

    private func summaryBox(title: String, value: String) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(title)
                .font(.system(size: 11, weight: .medium))
                .foregroundColor(HCTheme.textMuted)
            Text(value)
                .font(.system(size: 14, weight: .semibold).monospacedDigit())
                .foregroundColor(HCTheme.textPrimary)
                .lineLimit(1)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 10)
        .padding(.vertical, 7)
        .background(RoundedRectangle(cornerRadius: 9).fill(HCTheme.card))
        .overlay(RoundedRectangle(cornerRadius: 9).stroke(HCTheme.border, lineWidth: 1.2))
    }

    private func formatTotals(_ arr: [CurrencyTotal]?) -> String {
        guard let arr, !arr.isEmpty else { return "" }
        return arr.map { String(format: "%@%.2f", $0.symbol, $0.amount) }.joined(separator: " · ")
    }

    // MARK: - Tab Bar

    private var tabBar: some View {
        HStack(spacing: 6) {
            ForEach(PopoverTab.allCases, id: \.rawValue) { tab in
                let selected = activeTab == tab
                Button {
                    activeTab = tab
                } label: {
                    Text(tab.rawValue)
                        .font(.system(size: 12, weight: .semibold))
                        .foregroundColor(selected ? HCTheme.accent : HCTheme.textMuted)
                        .padding(.horizontal, 10)
                        .padding(.vertical, 5)
                        .background(
                            Capsule().fill(selected ? HCTheme.cardElevated : Color.clear)
                        )
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
            }
            Spacer()
        }
        .padding(.horizontal, 12)
        .padding(.bottom, 8)
    }

    // MARK: - Entries Scroll List

    private var initialErrorEntries: [EntrySnapshot] {
        (state.snapshot?.entries ?? []).filter { $0.isInitialError }
    }

    private var filteredEntries: [EntrySnapshot] {
        // Hide initial-error entries (never acquired quota/balance) from the menu bar list;
        // entries that had quota/balance and later became abnormal (stale/error) remain shown.
        (state.snapshot?.entries ?? []).filter { !$0.isInitialError && activeTab.includes($0) }
    }

    private var entriesScrollList: some View {
        ScrollView {
            VStack(spacing: 9) {
                if let err = state.errorMessage, state.snapshot == nil {
                    VStack(alignment: .leading, spacing: 8) {
                        Text("未连接到本地/内网 SubBar Server")
                            .font(.system(size: 13.5, weight: .semibold))
                            .foregroundColor(HCTheme.crit)
                        Text(err)
                            .font(.system(size: 12, weight: .medium))
                            .foregroundColor(HCTheme.textSecondary)
                        Button("重试连接") {
                            Task { await state.loadAll() }
                        }
                        .buttonStyle(.borderedProminent)
                    }
                    .padding(12)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(RoundedRectangle(cornerRadius: 11).fill(HCTheme.card))
                    .overlay(RoundedRectangle(cornerRadius: 11).stroke(HCTheme.crit, lineWidth: 1.5))
                } else {
                    discoverBanner
                    let pending = initialErrorEntries.filter(activeTab.includes)
                    if !pending.isEmpty {
                        Button {
                            activeTab = .prefs
                        } label: {
                            HStack {
                                MaterialIcon.warning.label("\(pending.count) 个入口首次同步失败")
                                Spacer()
                                Text("查看原因")
                            }
                            .font(.system(size: 12, weight: .semibold))
                            .foregroundStyle(HCTheme.warn)
                            .padding(10)
                            .background(HCTheme.card, in: RoundedRectangle(cornerRadius: 9))
                        }.buttonStyle(.plain)
                    }
                    if state.snapshot == nil {
                        ProgressView("正在获取订阅与余额…").padding()
                    } else if filteredEntries.isEmpty && pending.isEmpty {
                        emptyPane
                    } else {
                        ForEach(filteredEntries) { item in
                            EntryCardView(
                                item: item,
                                showRemaining: state.showRemaining,
                                now: nowTick,
                                onTogglePin: {
                                    Task { await state.quickPatch(id: item.id, payload: ["pinned": !item.pinned]) }
                                },
                                onRefresh: {
                                    Task { await state.triggerRefreshOne(id: item.id, name: item.name) }
                                },
                                onEdit: {
                                    editingEntryID = item.id
                                    showEditor = true
                                }
                            )
                        }
                    }
                }
            }
            .padding(12)
        }
    }

    @ViewBuilder
    private var discoverBanner: some View {
        let existingProviders = Set((state.snapshot?.entries ?? []).map { $0.provider })
        let pending = state.discoverHints.filter { !existingProviders.contains($0.provider) }
        if !pending.isEmpty {
            VStack(alignment: .leading, spacing: 6) {
                Text("检测到本机已登录账号（可免浏览器一键同步）：")
                    .font(.system(size: 11.5, weight: .medium))
                    .foregroundColor(HCTheme.accent)
                ForEach(pending) { hint in
                    Button {
                        Task {
                            let activePinnedCount = (state.snapshot?.entries ?? []).filter { !$0.isInitialError && $0.pinned }.count
                            await state.importDiscovery(
                                hintID: hint.id,
                                suggestedName: hint.suggestedName,
                                pinned: activePinnedCount < 2
                            )
                        }
                    } label: {
                        HStack {
                            MaterialIcon.link.label("同步 \(hint.suggestedName)")
                                .font(.system(size: 12, weight: .medium))
                                .foregroundColor(HCTheme.textPrimary)
                            Spacer()
                        }
                        .padding(.horizontal, 10)
                        .padding(.vertical, 6)
                        .background(RoundedRectangle(cornerRadius: 8).fill(HCTheme.cardElevated))
                        .overlay(RoundedRectangle(cornerRadius: 8).stroke(HCTheme.accent, lineWidth: 1))
                    }
                    .buttonStyle(.plain)
                }
            }
            .padding(10)
            .background(RoundedRectangle(cornerRadius: 10).fill(HCTheme.card))
            .overlay(RoundedRectangle(cornerRadius: 10).stroke(HCTheme.accent.opacity(0.7), style: StrokeStyle(lineWidth: 1.2, dash: [5, 3])))
        }
    }

    private var emptyPane: some View {
        VStack(spacing: 10) {
            Text(activeTab == .all ? "还没有订阅或余额" : "暂无\(activeTab.rawValue)")
                .font(.system(size: 15, weight: .semibold))
                .foregroundColor(HCTheme.textPrimary)
            Text("添加账号后，即可查看订阅用量、下次重置时间或 API 可用余额。")
                .font(.system(size: 12, weight: .medium))
                .foregroundColor(HCTheme.textMuted)
                .multilineTextAlignment(.center)
            Button {
                editingEntryID = nil
                showEditor = true
            } label: {
                MaterialIcon.add.label("添加订阅或余额")
                    .font(.system(size: 12.5, weight: .semibold))
                    .foregroundColor(HCTheme.bg)
                    .padding(.horizontal, 14)
                    .padding(.vertical, 8)
                    .background(RoundedRectangle(cornerRadius: 8).fill(HCTheme.accent))
            }
            .buttonStyle(.plain)
        }
        .padding(24)
        .frame(maxWidth: .infinity)
        .background(RoundedRectangle(cornerRadius: 12).fill(HCTheme.card))
        .overlay(RoundedRectangle(cornerRadius: 12).stroke(HCTheme.border, lineWidth: 1.2))
    }

    // MARK: - Preferences Pane

    private var preferencesPane: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                if !initialErrorEntries.isEmpty {
                    VStack(alignment: .leading, spacing: 8) {
                        Text("首次同步失败 (\(initialErrorEntries.count))")
                            .font(.system(size: 12.5, weight: .semibold))
                            .foregroundColor(HCTheme.textSecondary)
                        Text("检查账号设置或重试同步。成功取得数据后，入口会自动出现在列表中。")
                            .font(.system(size: 11))
                            .foregroundColor(HCTheme.textMuted)
                        ForEach(initialErrorEntries) { item in
                            HStack(spacing: 8) {
                                Text(item.badge)
                                    .font(.system(size: 10.5, weight: .bold, design: .monospaced))
                                    .foregroundColor(HCTheme.textPrimary)
                                    .padding(.horizontal, 5)
                                    .padding(.vertical, 2)
                                    .background(RoundedRectangle(cornerRadius: 5).fill(HCTheme.cardElevated))
                                VStack(alignment: .leading, spacing: 1) {
                                    Text(item.name)
                                        .font(.system(size: 12, weight: .medium))
                                        .foregroundColor(HCTheme.textPrimary)
                                        .lineLimit(1)
                                    if let err = item.error, !err.isEmpty {
                                        Text(err)
                                            .font(.system(size: 10.5))
                                            .foregroundColor(HCTheme.crit)
                                            .lineLimit(1)
                                    }
                                }
                                Spacer()
                                IconButton(icon: .refresh, title: "重试同步 \(item.name)") {
                                    Task { await state.triggerRefreshOne(id: item.id, name: item.name) }
                                }
                                IconButton(icon: .edit, title: "编辑 \(item.name)") {
                                    editingEntryID = item.id
                                    showEditor = true
                                }
                            }
                            .padding(8)
                            .background(RoundedRectangle(cornerRadius: 8).fill(HCTheme.bg))
                            .overlay(RoundedRectangle(cornerRadius: 8).stroke(HCTheme.border, lineWidth: 1))
                        }
                    }
                    .padding(12)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(RoundedRectangle(cornerRadius: 10).fill(HCTheme.card))
                    .overlay(RoundedRectangle(cornerRadius: 10).stroke(HCTheme.border, lineWidth: 1.2))
                }

                VStack(alignment: .leading, spacing: 6) {
                    Text("菜单栏显示方式")
                        .font(.system(size: 12.5, weight: .semibold))
                        .foregroundColor(HCTheme.textSecondary)
                    Picker("", selection: $state.menuBarStyle) {
                        ForEach(MenuBarDisplayStyle.allCases) { style in
                            Text(style.label).tag(style)
                        }
                    }
                    .labelsHidden()
                    .pickerStyle(.radioGroup)
                    Text("点击卡片上的图钉可在菜单栏显示该账号。双行模式最多显示两个账号。")
                        .font(.system(size: 11.5))
                        .foregroundColor(HCTheme.textMuted)
                }
                .padding(12)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(RoundedRectangle(cornerRadius: 10).fill(HCTheme.card))
                .overlay(RoundedRectangle(cornerRadius: 10).stroke(HCTheme.border, lineWidth: 1.2))

                VStack(alignment: .leading, spacing: 8) {
                    Text("服务连接")
                        .font(.system(size: 12.5, weight: .semibold))
                        .foregroundColor(HCTheme.textSecondary)
                    TextField("http://127.0.0.1:8328", text: $state.serverURL)
                        .textFieldStyle(.roundedBorder)
                    HStack {
                        Text("客户端同步间隔 (秒):")
                            .font(.system(size: 12, weight: .medium))
                            .foregroundColor(HCTheme.textSecondary)
                        Stepper("\(state.pollSeconds)s", value: $state.pollSeconds, in: 10...300, step: 10)
                            .font(.system(size: 12, weight: .medium).monospacedDigit())
                        Spacer()
                        Button("重新连接") {
                            Task { await state.loadAll() }
                        }
                        .buttonStyle(.borderedProminent)
                    }
                }
                .padding(12)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(RoundedRectangle(cornerRadius: 10).fill(HCTheme.card))
                .overlay(RoundedRectangle(cornerRadius: 10).stroke(HCTheme.border, lineWidth: 1.2))

                HStack {
                    Toggle(isOn: Binding(
                        get: { state.launchAtLoginEnabled },
                        set: { state.setLaunchAtLogin($0) }
                    )) {
                        Text("登录 macOS 时自动启动菜单栏")
                            .font(.system(size: 12.5, weight: .medium))
                            .foregroundColor(HCTheme.textPrimary)
                    }
                    Spacer()
                }
                .padding(12)
                .background(RoundedRectangle(cornerRadius: 10).fill(HCTheme.card))
                .overlay(RoundedRectangle(cornerRadius: 10).stroke(HCTheme.border, lineWidth: 1.2))


            }
            .padding(12)
        }
    }

    // MARK: - Footer

    private var footerBar: some View {
        HStack {
            Button {
                if let url = state.webDashboardURL() {
                    NSWorkspace.shared.open(url)
                }
            } label: {
                MaterialIcon.open.label("打开 Web 面板", size: 14)
                    .font(.system(size: 12, weight: .medium))
                    .foregroundColor(HCTheme.accent)
            }
            .buttonStyle(.plain)

            Spacer()

            Button {
                NSApplication.shared.terminate(nil)
            } label: {
                Text("退出")
                    .font(.system(size: 12, weight: .medium))
                    .foregroundColor(HCTheme.textMuted)
            }
            .buttonStyle(.plain)
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
        .background(HCTheme.card)
        .overlay(Rectangle().frame(height: 1).foregroundColor(HCTheme.border), alignment: .top)
    }
}

// MARK: - Individual High-Contrast Card

struct EntryCardView: View {
    let item: EntrySnapshot
    let showRemaining: Bool
    let now: Date
    let onTogglePin: () -> Void
    let onRefresh: () -> Void
    let onEdit: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 9) {
            // Card Header
            HStack(alignment: .top, spacing: 8) {
                Text(item.badge)
                    .font(.system(size: 11, weight: .bold, design: .monospaced))
                    .foregroundColor(HCTheme.textPrimary)
                    .padding(.horizontal, 6)
                    .padding(.vertical, 3)
                    .background(RoundedRectangle(cornerRadius: 6).fill(HCTheme.cardElevated))
                    .overlay(RoundedRectangle(cornerRadius: 6).stroke(HCTheme.border, lineWidth: 1.2))

                VStack(alignment: .leading, spacing: 2) {
                    HStack(spacing: 5) {
                        Text(item.name)
                            .font(.system(size: 13.5, weight: .semibold))
                            .foregroundColor(HCTheme.textPrimary)
                            .lineLimit(1)
                        if let plan = item.planLabel, !plan.isEmpty {
                            Text(plan)
                                .font(.system(size: 10.5, weight: .medium))
                                .foregroundColor(HCTheme.textSecondary)
                                .padding(.horizontal, 5)
                                .padding(.vertical, 1)
                                .background(RoundedRectangle(cornerRadius: 4).fill(HCTheme.cardElevated))
                        }
                    }
                    Text(
                        (item.accountEmail?.isEmpty == false)
                            ? "\(item.providerLabel) · \(item.accountEmail!)"
                            : item.providerLabel
                    )
                    .font(.system(size: 11, weight: .medium))
                    .foregroundColor(HCTheme.textMuted)
                    .lineLimit(1)
                }

                Spacer()

                HStack(spacing: 4) {
                    IconButton(icon: .pin, title: item.pinned ? "取消菜单栏置顶" : "置顶到菜单栏", selected: item.pinned, action: onTogglePin)
                    IconButton(icon: .refresh, title: "刷新 \(item.name)", action: onRefresh)
                    IconButton(icon: .edit, title: "编辑 \(item.name)", action: onEdit)
                }
            }

            // Subscription Quota Windows
            if let windows = item.windows, !windows.isEmpty {
                VStack(spacing: 8) {
                    ForEach(windows) { w in
                        windowRow(w)
                    }
                }
            }

            // Prepaid / API Balance
            if let bal = item.balance {
                VStack(alignment: .leading, spacing: 5) {
                    HStack(alignment: .firstTextBaseline) {
                        Text(String(format: "%@%.2f", bal.symbol, bal.available))
                            .font(.system(size: 22, weight: .bold).monospacedDigit())
                            .foregroundColor(HCTheme.textPrimary)
                        Spacer()
                        if let lim = bal.limit, lim > 0 {
                            Text(String(format: "总额 %@%.2f", bal.symbol, lim))
                                .font(.system(size: 11.5, weight: .medium).monospacedDigit())
                                .foregroundColor(HCTheme.textMuted)
                        }
                    }
                    if let details = bal.details, !details.isEmpty {
                        HStack(spacing: 6) {
                            ForEach(details.prefix(3)) { d in
                                Text("\(d.label) \(d.value)")
                                    .font(.system(size: 11, weight: .medium).monospacedDigit())
                                    .foregroundColor(HCTheme.textSecondary)
                                    .padding(.horizontal, 6)
                                    .padding(.vertical, 2.5)
                                    .background(RoundedRectangle(cornerRadius: 5).fill(HCTheme.cardElevated))
                                    .overlay(RoundedRectangle(cornerRadius: 5).stroke(HCTheme.border, lineWidth: 1))
                            }
                        }
                    }
                }
            }

            if let err = item.error, !err.isEmpty {
                Text(err)
                    .font(.system(size: 11.5, weight: .semibold))
                    .foregroundColor(HCTheme.crit)
                    .padding(7)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(RoundedRectangle(cornerRadius: 6).fill(HCTheme.crit.opacity(0.15)))
                    .overlay(RoundedRectangle(cornerRadius: 6).stroke(HCTheme.crit, lineWidth: 1))
            }

            // Bottom subtle bar
            HStack {
                Circle()
                    .fill(HCTheme.statusColor(item.status))
                    .frame(width: 7, height: 7)
                Text(item.statusReason?.isEmpty == false ? item.statusReason! : TimeFmt.relativeAgo(item.fetchedAt, now: now))
                    .font(.system(size: 11, weight: .semibold))
                    .foregroundColor(HCTheme.textMuted)
                Spacer()
                if let dash = item.dashboardUrl, let url = URL(string: dash) {
                    Button {
                        NSWorkspace.shared.open(url)
                    } label: {
                        MaterialIcon.open.label("官网", size: 13)
                            .font(.system(size: 11, weight: .medium))
                            .foregroundColor(HCTheme.accent)
                    }
                    .buttonStyle(.plain)
                }
            }
        }
        .padding(11)
        .background(RoundedRectangle(cornerRadius: 11).fill(HCTheme.card))
        .overlay(
            RoundedRectangle(cornerRadius: 11)
                .stroke(borderColorForStatus(item.status), lineWidth: 1.4)
        )
        .opacity(item.enabled ? 1.0 : 0.65)
    }

    private func borderColorForStatus(_ status: String) -> Color {
        switch status {
        case "critical", "error": return HCTheme.crit
        case "warning", "stale": return HCTheme.warn
        default: return HCTheme.border
        }
    }

    private func windowRow(_ w: QuotaWindow) -> some View {
        let isCycle = w.key == "cycle"
        let shownPct = (showRemaining && !isCycle) ? w.remainingPercent : w.usedPercent
        let metricText: String = {
            if isCycle { return w.usedDisplay ?? String(format: "%.0f%%", w.usedPercent) }
            let pctStr = showRemaining ? String(format: "剩余 %.0f%%", shownPct) : String(format: "已用 %.0f%%", shownPct)
            if w.key == "quota", let disp = w.usedDisplay { return "\(disp) · \(pctStr)" }
            return pctStr
        }()

        let barColor: Color = {
            if isCycle { return HCTheme.accent }
            if w.usedPercent >= 90 { return HCTheme.crit }
            if w.usedPercent >= 75 { return HCTheme.warn }
            return HCTheme.ok
        }()

        return VStack(alignment: .leading, spacing: 4) {
            HStack {
                Text(w.label)
                    .font(.system(size: 12, weight: .medium))
                    .foregroundColor(HCTheme.textSecondary)
                Spacer()
                Text(metricText)
                    .font(.system(size: 12, weight: .semibold).monospacedDigit())
                    .foregroundColor(HCTheme.textPrimary)
            }

            GeometryReader { geo in
                ZStack(alignment: .leading) {
                    Capsule()
                        .fill(HCTheme.bg)
                        .overlay(Capsule().stroke(HCTheme.border, lineWidth: 1.2))
                    Capsule()
                        .fill(barColor)
                        .frame(width: geo.size.width * CGFloat(max(0, min(100, shownPct)) / 100.0))

                    if let pace = w.paceDeltaPercent {
                        let elapsed = max(0, min(100, w.usedPercent - pace))
                        let markerPct = showRemaining ? (100 - elapsed) : elapsed
                        Rectangle()
                            .fill(HCTheme.textPrimary)
                            .frame(width: 2, height: geo.size.height)
                            .offset(x: geo.size.width * CGFloat(markerPct / 100.0))
                    }
                }
            }
            .frame(height: 10)

            HStack {
                let cd = TimeFmt.countdown(to: w.resetsAt, now: now)
                if !cd.isEmpty {
                    Text(cd)
                        .font(.system(size: 11, weight: .semibold).monospacedDigit())
                        .foregroundColor(HCTheme.textMuted)
                }
                Spacer()
                if let pace = w.paceDeltaPercent {
                    Text(pace > 8 ? String(format: "超前 +%.0f%%", pace) : "节奏稳健")
                        .font(.system(size: 11, weight: .medium).monospacedDigit())
                        .foregroundColor(pace > 8 ? HCTheme.warn : HCTheme.ok)
                }
            }
        }
    }
}
