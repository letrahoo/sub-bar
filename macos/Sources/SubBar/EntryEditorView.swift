import SwiftUI
import AppKit

struct EntryEditorView: View {
    @ObservedObject var state: AppState
    let entryID: String?
    let onClose: () -> Void

    @State private var providerID: String = "antigravity_oauth"
    @State private var name: String = ""
    @State private var badge: String = ""
    @State private var kind: String = "subscription"
    @State private var pinned: Bool = true
    @State private var enabled: Bool = true
    @State private var refreshSeconds: String = "300"
    @State private var monthlyCost: String = ""
    @State private var costCurrency: String = "USD"
    @State private var dashboardUrl: String = ""
    @State private var notes: String = ""
    @State private var fieldValues: [String: String] = [:]
    @State private var isSaving = false
    @State private var confirmDelete = false

    // Interactive OAuth Link + Polling state
    @State private var oauthSession: OAuthStartResponse? = nil
    @State private var isStartingOAuth = false
    @State private var oauthStatusText: String = ""
    @State private var oauthIsError = false
    @State private var callbackPasteInput: String = ""
    @State private var showCallbackPaste = false
    @State private var isSubmittingPaste = false
    @State private var pollTask: Task<Void, Never>? = nil

    private var currentDescriptor: ProviderDescriptor? {
        state.providers.first(where: { $0.id == providerID }) ?? state.providers.first
    }

    private var existingConfig: EntryConfigPublic? {
        guard let id = entryID else { return nil }
        return state.entriesConfig[id]
    }

    private var isOAuthProvider: Bool {
        currentDescriptor?.supportsOAuthFlow == true
    }

    private var hasManualSecret: Bool {
        !(fieldValues["secret"] ?? "").trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    var body: some View {
        VStack(spacing: 0) {
            HStack {
                Text(existingConfig == nil ? "添加订阅 / 余额入口" : "编辑入口 · \(existingConfig?.name ?? "")")
                    .font(.system(size: 15, weight: .heavy))
                    .foregroundColor(HCTheme.textPrimary)
                Spacer()
                Button("关闭") {
                    stopPolling()
                    onClose()
                }
                .buttonStyle(.plain)
                .foregroundColor(HCTheme.textMuted)
            }
            .padding(14)
            .background(HCTheme.card)

            Divider().background(HCTheme.border)

            ScrollView {
                VStack(alignment: .leading, spacing: 12) {
                    VStack(alignment: .leading, spacing: 4) {
                        Text("服务 / 数据源类型")
                            .font(.system(size: 12, weight: .bold))
                            .foregroundColor(HCTheme.textSecondary)
                        Picker("", selection: $providerID) {
                            ForEach(state.providers) { p in
                                Text(p.name).tag(p.id)
                            }
                        }
                        .labelsHidden()
                        .onChange(of: providerID) { _, newValue in
                            stopPolling()
                            if existingConfig == nil {
                                applyDefaults(for: newValue)
                            }
                        }
                        if let desc = currentDescriptor {
                            Text(desc.description)
                                .font(.system(size: 11.5))
                                .foregroundColor(HCTheme.textMuted)
                        }
                    }

                    HStack(spacing: 8) {
                        VStack(alignment: .leading, spacing: 4) {
                            Text(isOAuthProvider && existingConfig == nil ? "显示名称 (可选，留空自动按邮箱命名)" : "显示名称 *")
                                .font(.system(size: 12, weight: .bold))
                                .foregroundColor(HCTheme.textSecondary)
                            TextField(
                                isOAuthProvider && existingConfig == nil
                                    ? "留空自动命名，如 Antigravity (邮箱前缀)"
                                    : "如 Antigravity 主力 / DeepSeek API",
                                text: $name
                            )
                            .textFieldStyle(.roundedBorder)
                        }
                        VStack(alignment: .leading, spacing: 4) {
                            Text("状态栏短标")
                                .font(.system(size: 12, weight: .bold))
                                .foregroundColor(HCTheme.textSecondary)
                            TextField("AGY", text: $badge)
                                .textFieldStyle(.roundedBorder)
                                .frame(width: 82)
                        }
                    }

                    if isOAuthProvider {
                        oauthOnboardingCard
                    }

                    if currentDescriptor?.supportsKindSwitch == true {
                        VStack(alignment: .leading, spacing: 4) {
                            Text("计量模式")
                                .font(.system(size: 12, weight: .bold))
                                .foregroundColor(HCTheme.textSecondary)
                            Picker("", selection: $kind) {
                                Text("订阅配额 / 周期重置").tag("subscription")
                                Text("储值 / API 余额").tag("balance")
                            }
                            .pickerStyle(.segmented)
                            .labelsHidden()
                        }
                    }

                    if let fields = currentDescriptor?.fields, !fields.isEmpty {
                        ForEach(fields) { f in
                            dynamicFieldView(f)
                        }
                    }

                    Divider().background(HCTheme.border)

                    Toggle("置顶显示在 Mac 顶部菜单栏", isOn: $pinned)
                        .font(.system(size: 12.5, weight: .bold))
                    Toggle("启用自动轮询刷新", isOn: $enabled)
                        .font(.system(size: 12.5, weight: .bold))

                    HStack(spacing: 8) {
                        VStack(alignment: .leading, spacing: 4) {
                            Text("固定月费折算 (可选)")
                                .font(.system(size: 12, weight: .bold))
                                .foregroundColor(HCTheme.textSecondary)
                            TextField("20.00", text: $monthlyCost)
                                .textFieldStyle(.roundedBorder)
                        }
                        VStack(alignment: .leading, spacing: 4) {
                            Text("月费币种")
                                .font(.system(size: 12, weight: .bold))
                                .foregroundColor(HCTheme.textSecondary)
                            Picker("", selection: $costCurrency) {
                                Text("USD ($)").tag("USD")
                                Text("CNY (¥)").tag("CNY")
                            }
                            .labelsHidden()
                            .frame(width: 98)
                        }
                        VStack(alignment: .leading, spacing: 4) {
                            Text("刷新间隔(秒)")
                                .font(.system(size: 12, weight: .bold))
                                .foregroundColor(HCTheme.textSecondary)
                            TextField("300", text: $refreshSeconds)
                                .textFieldStyle(.roundedBorder)
                                .frame(width: 80)
                        }
                    }

                    VStack(alignment: .leading, spacing: 4) {
                        Text("官网账单页链接 (可选)")
                            .font(.system(size: 12, weight: .bold))
                            .foregroundColor(HCTheme.textSecondary)
                        TextField("https://...", text: $dashboardUrl)
                            .textFieldStyle(.roundedBorder)
                    }
                }
                .padding(14)
            }

            Divider().background(HCTheme.border)

            HStack {
                if let id = entryID {
                    if confirmDelete {
                        Button("确认彻底删除") {
                            Task {
                                if await state.deleteEntry(id: id) {
                                    stopPolling()
                                    onClose()
                                }
                            }
                        }
                        .buttonStyle(.borderedProminent)
                        .tint(HCTheme.crit)
                    } else {
                        Button("删除") { confirmDelete = true }
                            .foregroundColor(HCTheme.crit)
                    }
                }
                Spacer()
                Button("取消") {
                    stopPolling()
                    onClose()
                }
                if !isOAuthProvider || existingConfig != nil || hasManualSecret {
                    Button(isSaving ? "保存中…" : "保存并同步") {
                        Task { await submit() }
                    }
                    .buttonStyle(.borderedProminent)
                    .disabled(isSaving || (!isOAuthProvider && name.trimmingCharacters(in: .whitespaces).isEmpty))
                }
            }
            .padding(12)
            .background(HCTheme.card)
        }
        .frame(width: 392, height: 520)
        .background(HCTheme.bg)
        .tint(HCTheme.accent)
        .preferredColorScheme(.dark)
        .onAppear { populateInitial() }
        .onDisappear { stopPolling() }
    }

    // MARK: - CLIProxyAPI-style 1-Click OAuth Panel

    private var oauthOnboardingCard: some View {
        VStack(alignment: .leading, spacing: 9) {
            if let cfg = existingConfig, cfg.hasSecret {
                HStack(spacing: 6) {
                    MaterialIcon.check.label("已授权")
                        .font(.system(size: 12, weight: .heavy))
                        .foregroundColor(HCTheme.ok)
                    if let em = cfg.accountEmail, !em.isEmpty {
                        Text("· \(em)")
                            .font(.system(size: 11.5, weight: .bold))
                            .foregroundColor(HCTheme.textPrimary)
                            .lineLimit(1)
                    }
                    if cfg.hasRefreshToken == true {
                        Text("· 自动续期")
                            .font(.system(size: 11, weight: .bold))
                            .foregroundColor(HCTheme.accent)
                    }
                    Spacer()
                }
                .padding(.horizontal, 10)
                .padding(.vertical, 6)
                .background(RoundedRectangle(cornerRadius: 7).fill(HCTheme.ok.opacity(0.16)))
                .overlay(RoundedRectangle(cornerRadius: 7).stroke(HCTheme.ok, lineWidth: 1.2))
            }

            let primaryLabel: String = {
                if existingConfig?.hasSecret == true {
                    return isStartingOAuth ? "正在生成链接…" : "重新登录授权 / 切换账号"
                }
                let base = currentDescriptor?.oauthButtonLabel ?? "打开浏览器授权链接"
                return isStartingOAuth ? "正在生成链接…" : "\(base)"
            }()

            Button {
                Task { await beginOAuthFlow() }
            } label: {
                HStack {
                    Spacer()
                    MaterialIcon.link.label(primaryLabel)
                        .font(.system(size: 13, weight: .heavy))
                        .foregroundColor(HCTheme.bg)
                    Spacer()
                }
                .padding(.vertical, 8)
                .background(RoundedRectangle(cornerRadius: 8).fill(HCTheme.accent))
            }
            .buttonStyle(.plain)
            .disabled(isStartingOAuth)

            if let sess = oauthSession {
                HStack(spacing: 6) {
                    Button {
                        if let u = URL(string: sess.authUrl) {
                            NSWorkspace.shared.open(u)
                        }
                    } label: {
                        MaterialIcon.open.label("再次打开浏览器", size: 14)
                            .font(.system(size: 11.5, weight: .bold))
                            .foregroundColor(HCTheme.textPrimary)
                            .padding(.horizontal, 9)
                            .padding(.vertical, 5)
                            .background(RoundedRectangle(cornerRadius: 6).fill(HCTheme.cardElevated))
                            .overlay(RoundedRectangle(cornerRadius: 6).stroke(HCTheme.border, lineWidth: 1))
                    }
                    .buttonStyle(.plain)

                    Button {
                        NSPasteboard.general.clearContents()
                        NSPasteboard.general.setString(sess.authUrl, forType: .string)
                        state.showToast("已复制授权链接")
                    } label: {
                        Text("复制链接")
                            .font(.system(size: 11.5, weight: .bold))
                            .foregroundColor(HCTheme.textPrimary)
                            .padding(.horizontal, 9)
                            .padding(.vertical, 5)
                            .background(RoundedRectangle(cornerRadius: 6).fill(HCTheme.cardElevated))
                            .overlay(RoundedRectangle(cornerRadius: 6).stroke(HCTheme.border, lineWidth: 1))
                    }
                    .buttonStyle(.plain)

                    if let ucode = sess.userCode, !ucode.isEmpty {
                        Text("确认码 \(ucode)")
                            .font(.system(size: 11, weight: .heavy, design: .monospaced))
                            .foregroundColor(HCTheme.accent)
                            .padding(.horizontal, 7)
                            .padding(.vertical, 4)
                            .background(RoundedRectangle(cornerRadius: 6).fill(HCTheme.bg))
                            .overlay(RoundedRectangle(cornerRadius: 6).stroke(HCTheme.accent.opacity(0.7), lineWidth: 1))
                    }

                    Spacer()
                }

                HStack(spacing: 7) {
                    Circle()
                        .fill(oauthIsError ? HCTheme.crit : HCTheme.accent)
                        .frame(width: 8, height: 8)
                    Text(oauthStatusText)
                        .font(.system(size: 11.5, weight: .bold))
                        .foregroundColor(oauthIsError ? HCTheme.crit : HCTheme.textSecondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
                .padding(8)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(RoundedRectangle(cornerRadius: 7).fill(HCTheme.bg))
                .overlay(
                    RoundedRectangle(cornerRadius: 7)
                        .stroke(oauthIsError ? HCTheme.crit : HCTheme.border, lineWidth: 1.2)
                )

                if sess.callbackMode != "device" {
                    DisclosureGroup("在另一台设备打开？粘贴回调地址完成绑定", isExpanded: $showCallbackPaste) {
                        VStack(alignment: .leading, spacing: 6) {
                            Text("若在远程 Server 或其他浏览器完成登录，复制跳转后的 http://localhost:.../?code=... 完整地址粘贴到下方：")
                                .font(.system(size: 11))
                                .foregroundColor(HCTheme.textMuted)
                            HStack(spacing: 6) {
                                TextField("http://localhost:.../?code=...", text: $callbackPasteInput)
                                    .textFieldStyle(.roundedBorder)
                                Button(isSubmittingPaste ? "验证中…" : "提交") {
                                    Task { await submitManualCallback() }
                                }
                                .buttonStyle(.borderedProminent)
                                .disabled(isSubmittingPaste || callbackPasteInput.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                            }
                        }
                        .padding(.top, 6)
                    }
                    .font(.system(size: 11.5, weight: .bold))
                    .foregroundColor(HCTheme.accent)
                }
            }
        }
        .padding(11)
        .background(RoundedRectangle(cornerRadius: 10).fill(HCTheme.card))
        .overlay(RoundedRectangle(cornerRadius: 10).stroke(HCTheme.accent, lineWidth: 1.5))
    }

    private func buildBasePayload() -> [String: Any] {
        let desc = currentDescriptor
        var resolvedName = name.trimmingCharacters(in: .whitespacesAndNewlines)
        if resolvedName.isEmpty && (existingConfig != nil || hasManualSecret) {
            resolvedName = existingConfig?.name ?? desc?.name.components(separatedBy: " ").first ?? "订阅账号"
        }
        var body: [String: Any] = [
            "name": resolvedName,
            "provider": providerID,
            "kind": (desc?.supportsKindSwitch == true) ? kind : (desc?.defaultKind ?? "subscription"),
            "badge": badge.trimmingCharacters(in: .whitespacesAndNewlines),
            "pinned": pinned,
            "enabled": enabled,
            "refreshSeconds": Int(refreshSeconds) ?? 300,
            "costCurrency": costCurrency,
            "dashboardUrl": dashboardUrl.trimmingCharacters(in: .whitespacesAndNewlines),
            "notes": notes.trimmingCharacters(in: .whitespacesAndNewlines),
            "keepSecret": entryID != nil
        ]
        if let mc = Double(monthlyCost.trimmingCharacters(in: .whitespaces)), mc > 0 {
            body["monthlyCost"] = mc
        }
        for f in desc?.fields ?? [] {
            let raw = (fieldValues[f.key] ?? f.default ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
            if f.type == "number" {
                if !raw.isEmpty {
                    if f.key == "billingDay", let iv = Int(raw) {
                        body[f.key] = iv
                    } else if let dv = Double(raw) {
                        body[f.key] = dv
                    }
                }
            } else {
                body[f.key] = raw
            }
        }
        return body
    }

    private func beginOAuthFlow() async {
        stopPolling()
        isStartingOAuth = true
        oauthIsError = false
        defer { isStartingOAuth = false }

        var payload = buildBasePayload()
        if let id = entryID, !id.isEmpty {
            payload["entryId"] = id
        }

        do {
            let sess = try await state.startOAuth(payload: payload)
            self.oauthSession = sess
            self.callbackPasteInput = ""
            if sess.callbackMode == "device" {
                self.showCallbackPaste = false
                let codeNote = (sess.userCode?.isEmpty == false) ? "（确认码 \(sess.userCode!) 已带入）" : ""
                self.oauthStatusText = "已打开授权页\(codeNote)，支持微信扫码登录，网页点击确认后将自动同步关闭…"
            } else {
                let isLocal = state.isLoopbackServer && sess.callbackMode == "loopback"
                self.showCallbackPaste = !isLocal
                self.oauthStatusText = isLocal
                    ? "正在监听回调 (127.0.0.1:\(sess.callbackPort))… 浏览器授权后自动同步关闭"
                    : "请在浏览器完成授权，并在下方粘贴跳转后的回调地址"
            }

            if let u = URL(string: sess.authUrl) {
                NSWorkspace.shared.open(u)
            }

            let targetState = sess.state
            pollTask = Task { @MainActor in
                while !Task.isCancelled {
                    try? await Task.sleep(nanoseconds: 1_500_000_000)
                    if Task.isCancelled { break }
                    guard let res = try? await state.pollOAuthStatus(state: targetState) else { continue }
                    if res.status == "completed" {
                        await state.loadAll()
                        let label = res.entry?.name ?? res.accountEmail ?? "新账号"
                        state.showToast("✓ 授权成功，已接入 \(label)")
                        stopPolling()
                        onClose()
                        break
                    } else if res.status == "error" || res.status == "expired" {
                        oauthIsError = true
                        oauthStatusText = res.error ?? "授权已过期或被取消，请重新点击上方按钮"
                        break
                    } else if res.status == "exchanging" {
                        oauthStatusText = "已收到回调，正在换取令牌并拉取配额…"
                    }
                }
            }
        } catch {
            state.showToast(error.localizedDescription)
        }
    }

    private func submitManualCallback() async {
        guard let sess = oauthSession else { return }
        let raw = callbackPasteInput.trimmingCharacters(in: .whitespacesAndNewlines)
        if raw.isEmpty { return }
        isSubmittingPaste = true
        defer { isSubmittingPaste = false }

        do {
            let res = try await state.submitOAuthCallbackURL(state: sess.state, callbackURL: raw)
            if res.status == "completed" {
                await state.loadAll()
                let label = res.entry?.name ?? res.accountEmail ?? "新账号"
                state.showToast("✓ 授权成功，已接入 \(label)")
                stopPolling()
                onClose()
            }
        } catch {
            state.showToast(error.localizedDescription)
        }
    }

    private func stopPolling() {
        pollTask?.cancel()
        pollTask = nil
    }

    @ViewBuilder
    private func dynamicFieldView(_ field: ProviderFieldSchema) -> some View {
        let binding = Binding<String>(
            get: { fieldValues[field.key] ?? field.default ?? "" },
            set: { fieldValues[field.key] = $0 }
        )

        VStack(alignment: .leading, spacing: 4) {
            Text(field.label + (field.required == true ? " *" : ""))
                .font(.system(size: 12, weight: .bold))
                .foregroundColor(HCTheme.textSecondary)

            if field.type == "select", let opts = field.options {
                Picker("", selection: binding) {
                    ForEach(opts) { op in
                        Text(op.label).tag(op.value)
                    }
                }
                .labelsHidden()
            } else if field.type == "secret" {
                let placeholder = (existingConfig?.hasSecret == true)
                    ? "已保存 (\(existingConfig?.secretHint ?? "••••"))，留空保持不变"
                    : (field.placeholder ?? "")
                SecureField(placeholder, text: binding)
                    .textFieldStyle(.roundedBorder)
            } else {
                TextField(field.placeholder ?? "", text: binding)
                    .textFieldStyle(.roundedBorder)
            }

            if let help = field.help, !help.isEmpty {
                Text(help)
                    .font(.system(size: 11))
                    .foregroundColor(HCTheme.textMuted)
            }
        }
    }

    private func populateInitial() {
        if let cfg = existingConfig {
            providerID = cfg.provider
            name = cfg.name
            badge = cfg.badge ?? ""
            kind = cfg.kind
            pinned = cfg.pinned
            enabled = cfg.enabled
            refreshSeconds = String(cfg.refreshSeconds)
            if let mc = cfg.monthlyCost { monthlyCost = String(mc) }
            costCurrency = cfg.costCurrency ?? "USD"
            dashboardUrl = cfg.dashboardUrl ?? ""
            notes = cfg.notes ?? ""

            var fv: [String: String] = [:]
            if let v = cfg.authMode { fv["authMode"] = v }
            if let v = cfg.credentialFile { fv["credentialFile"] = v }
            if let v = cfg.accountId { fv["accountId"] = v }
            if let v = cfg.baseUrl { fv["baseUrl"] = v }
            if let v = cfg.method { fv["method"] = v }
            if let v = cfg.authHeader { fv["authHeader"] = v }
            if let v = cfg.authPrefix { fv["authPrefix"] = v }
            if let v = cfg.probeMode { fv["probeMode"] = v }
            if let v = cfg.probeModel { fv["probeModel"] = v }
            if let v = cfg.currency { fv["currency"] = v }
            if let v = cfg.scale, v != 0 { fv["scale"] = String(v) }
            if let v = cfg.lowThreshold { fv["lowThreshold"] = String(v) }
            if let v = cfg.balancePath { fv["balancePath"] = v }
            if let v = cfg.usedPath { fv["usedPath"] = v }
            if let v = cfg.limitPath { fv["limitPath"] = v }
            if let v = cfg.usedPercentPath { fv["usedPercentPath"] = v }
            if let v = cfg.resetAtPath { fv["resetAtPath"] = v }
            if let v = cfg.manualCycle { fv["manualCycle"] = v }
            if let v = cfg.billingDay, v > 0 { fv["billingDay"] = String(v) }
            if let v = cfg.nextResetDate { fv["nextResetDate"] = v }
            if let v = cfg.planLabel { fv["planLabel"] = v }
            if let v = cfg.quotaUsed { fv["quotaUsed"] = String(v) }
            if let v = cfg.quotaTotal { fv["quotaTotal"] = String(v) }
            if let v = cfg.quotaUnit { fv["quotaUnit"] = v }
            if let v = cfg.manualBalance { fv["manualBalance"] = String(v) }
            fieldValues = fv
        } else if let first = state.providers.first {
            providerID = first.id
            applyDefaults(for: first.id)
            let activePinnedCount = (state.snapshot?.entries ?? []).filter { !$0.isInitialError && $0.pinned }.count
            pinned = activePinnedCount < 2
        }
    }

    private func applyDefaults(for id: String) {
        oauthSession = nil
        oauthIsError = false
        guard let desc = state.providers.first(where: { $0.id == id }) else { return }
        badge = desc.defaultBadge
        kind = desc.defaultKind
        refreshSeconds = String(desc.defaultRefreshSeconds)
        var fv: [String: String] = [:]
        for f in desc.fields ?? [] {
            if let def = f.default {
                fv[f.key] = def
            }
        }
        fieldValues = fv
    }

    private func submit() async {
        isSaving = true
        defer { isSaving = false }

        let body = buildBasePayload()
        if await state.saveEntry(existingID: entryID, payload: body) {
            stopPolling()
            onClose()
        }
    }
}
