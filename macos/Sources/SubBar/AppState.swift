import Foundation
import SwiftUI
import ServiceManagement

@MainActor
final class AppState: ObservableObject {
    @Published var snapshot: SnapshotResponse?
    @Published var providers: [ProviderDescriptor] = []
    @Published var entriesConfig: [String: EntryConfigPublic] = [:]
    @Published var discoverHints: [DiscoveryHint] = []
    @Published var isLoading = false
    @Published var isEditorPresented = false
    @Published var errorMessage: String?
    @Published var toastMessage: String?

    @Published var serverURL: String {
        didSet { UserDefaults.standard.set(serverURL, forKey: "subbar.serverURL") }
    }
    @Published var pollSeconds: Int {
        didSet {
            UserDefaults.standard.set(pollSeconds, forKey: "subbar.pollSeconds")
            restartTimer()
        }
    }
    @Published var menuBarStyle: MenuBarDisplayStyle {
        didSet {
            UserDefaults.standard.set(menuBarStyle.rawValue, forKey: "subbar.menuBarStyle")
            onMenuBarNeedsUpdate?()
        }
    }
    @Published var showRemaining: Bool {
        didSet {
            UserDefaults.standard.set(showRemaining, forKey: "subbar.showRemaining")
            onMenuBarNeedsUpdate?()
        }
    }

    var onMenuBarNeedsUpdate: (() -> Void)?
    private var timer: Timer?

    init() {
        let defaults = UserDefaults.standard
        self.serverURL = defaults.string(forKey: "subbar.serverURL") ?? "http://127.0.0.1:8328"
        let savedPoll = defaults.integer(forKey: "subbar.pollSeconds")
        self.pollSeconds = savedPoll >= 10 ? savedPoll : 30
        let savedStyle = defaults.string(forKey: "subbar.menuBarStyle") ?? MenuBarDisplayStyle.stacked.rawValue
        self.menuBarStyle = MenuBarDisplayStyle(rawValue: savedStyle) ?? .stacked
        if defaults.object(forKey: "subbar.showRemaining") == nil {
            self.showRemaining = true
        } else {
            self.showRemaining = defaults.bool(forKey: "subbar.showRemaining")
        }
    }

    func start() {
        restartTimer()
        Task { await loadAll() }
    }

    private func restartTimer() {
        timer?.invalidate()
        let interval = TimeInterval(max(10, pollSeconds))
        timer = Timer.scheduledTimer(withTimeInterval: interval, repeats: true) { [weak self] _ in
            guard let self else { return }
            Task { @MainActor in
                await self.loadSnapshotOnly()
            }
        }
    }

    private func normalizedBaseURL() -> String {
        var s = serverURL.trimmingCharacters(in: .whitespacesAndNewlines)
        while s.hasSuffix("/") { s.removeLast() }
        return s.isEmpty ? "http://127.0.0.1:8328" : s
    }

    func webDashboardURL() -> URL? {
        URL(string: normalizedBaseURL() + "/")
    }

    func showToast(_ msg: String) {
        toastMessage = msg
        Task { @MainActor in
            try? await Task.sleep(nanoseconds: 2_300_000_000)
            if self.toastMessage == msg {
                self.toastMessage = nil
            }
        }
    }

    func loadSnapshotOnly() async {
        do {
            let snap: SnapshotResponse = try await requestJSON(path: "/api/snapshot")
            self.snapshot = snap
            self.errorMessage = nil
            self.onMenuBarNeedsUpdate?()
        } catch {
            self.errorMessage = error.localizedDescription
            self.onMenuBarNeedsUpdate?()
        }
    }

    func loadAll() async {
        isLoading = true
        defer { isLoading = false }
        do {
            async let snapTask: SnapshotResponse = requestJSON(path: "/api/snapshot")
            async let provTask: ProvidersEnvelope = requestJSON(path: "/api/providers")
            async let entTask: EntriesEnvelope = requestJSON(path: "/api/entries")
            let (snap, provEnv, entEnv) = try await (snapTask, provTask, entTask)

            self.snapshot = snap
            self.providers = provEnv.providers
            var map: [String: EntryConfigPublic] = [:]
            for e in entEnv.entries { map[e.id] = e }
            self.entriesConfig = map
            self.errorMessage = nil

            if let disc: DiscoverEnvelope = try? await requestJSON(path: "/api/discover") {
                self.discoverHints = disc.hints ?? []
            }
            self.onMenuBarNeedsUpdate?()
        } catch {
            self.errorMessage = "无法连接本机/内网 Server (\(normalizedBaseURL())): \(error.localizedDescription)"
            self.onMenuBarNeedsUpdate?()
        }
    }

    func triggerRefreshAll() async {
        isLoading = true
        defer { isLoading = false }
        do {
            let snap: SnapshotResponse = try await requestJSON(path: "/api/refresh", method: "POST")
            self.snapshot = snap
            self.errorMessage = nil
            self.onMenuBarNeedsUpdate?()
            showToast("全部入口已刷新")
        } catch {
            showToast("刷新失败: \(error.localizedDescription)")
        }
    }

    func triggerRefreshOne(id: String, name: String) async {
        do {
            let _: [String: EntrySnapshot] = try await requestJSON(
                path: "/api/entries/\(id.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? id)/refresh",
                method: "POST"
            )
            await loadAll()
            showToast("已刷新 \(name)")
        } catch {
            showToast("刷新失败: \(error.localizedDescription)")
        }
    }

    func quickPatch(id: String, payload: [String: Any]) async {
        do {
            let data = try JSONSerialization.data(withJSONObject: payload)
            try await sendRaw(
                path: "/api/entries/\(id.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? id)/quick-update",
                method: "POST",
                body: data
            )
            await loadAll()
        } catch {
            showToast("更新失败: \(error.localizedDescription)")
        }
    }

    func saveEntry(existingID: String?, payload: [String: Any]) async -> Bool {
        do {
            let data = try JSONSerialization.data(withJSONObject: payload)
            if let id = existingID, !id.isEmpty {
                let enc = id.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? id
                try await sendRaw(path: "/api/entries/\(enc)", method: "PUT", body: data)
            } else {
                try await sendRaw(path: "/api/entries", method: "POST", body: data)
            }
            await loadAll()
            showToast("已保存并同步")
            return true
        } catch {
            showToast(error.localizedDescription)
            return false
        }
    }

    func deleteEntry(id: String) async -> Bool {
        do {
            let enc = id.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? id
            try await sendRaw(path: "/api/entries/\(enc)", method: "DELETE", body: nil)
            await loadAll()
            showToast("已删除入口")
            return true
        } catch {
            showToast(error.localizedDescription)
            return false
        }
    }

    var isLoopbackServer: Bool {
        guard let u = URL(string: normalizedBaseURL()), let h = u.host?.lowercased() else { return true }
        return h == "127.0.0.1" || h == "localhost" || h == "::1"
    }

    func importDiscovery(hintID: String, suggestedName: String, pinned: Bool) async {
        do {
            let body = try JSONSerialization.data(withJSONObject: [
                "hintId": hintID,
                "pinned": pinned
            ])
            try await sendRaw(path: "/api/discover/import", method: "POST", body: body)
            await loadAll()
            showToast("已同步接入 \(suggestedName)")
        } catch {
            showToast(error.localizedDescription)
        }
    }

    func startOAuth(payload: [String: Any]) async throws -> OAuthStartResponse {
        let body = try JSONSerialization.data(withJSONObject: payload)
        return try await requestJSONWithBody(path: "/api/oauth/start", method: "POST", body: body)
    }

    func pollOAuthStatus(state: String) async throws -> OAuthStatusResponse {
        let enc = state.addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed) ?? state
        return try await requestJSON(path: "/api/oauth/status?state=\(enc)")
    }

    func submitOAuthCallbackURL(state: String, callbackURL: String) async throws -> OAuthStatusResponse {
        let body = try JSONSerialization.data(withJSONObject: [
            "state": state,
            "callbackUrl": callbackURL
        ])
        return try await requestJSONWithBody(path: "/api/oauth/callback-url", method: "POST", body: body)
    }

    // Launch at Login helper (macOS 13+)
    var launchAtLoginEnabled: Bool {
        SMAppService.mainApp.status == .enabled
    }

    func setLaunchAtLogin(_ enabled: Bool) {
        do {
            if enabled {
                try SMAppService.mainApp.register()
            } else {
                try SMAppService.mainApp.unregister()
            }
            objectWillChange.send()
        } catch {
            showToast("开机自启需将 SubBar.app 放入 /Applications 后开启")
        }
    }

    // MARK: - Networking

    private func requestJSON<T: Decodable>(path: String, method: String = "GET") async throws -> T {
        return try await requestJSONWithBody(path: path, method: method, body: nil)
    }

    private func requestJSONWithBody<T: Decodable>(path: String, method: String, body: Data?) async throws -> T {
        guard let url = URL(string: normalizedBaseURL() + path) else {
            throw URLError(.badURL)
        }
        var req = URLRequest(url: url)
        req.httpMethod = method
        req.timeoutInterval = 22
        req.setValue("application/json", forHTTPHeaderField: "Accept")
        if let body {
            req.httpBody = body
            req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }
        let (data, resp) = try await URLSession.shared.data(for: req)
        try validateHTTP(resp: resp, data: data)
        return try JSONDecoder().decode(T.self, from: data)
    }

    private func sendRaw(path: String, method: String, body: Data?) async throws {
        guard let url = URL(string: normalizedBaseURL() + path) else {
            throw URLError(.badURL)
        }
        var req = URLRequest(url: url)
        req.httpMethod = method
        req.timeoutInterval = 22
        if let body {
            req.httpBody = body
            req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }
        let (data, resp) = try await URLSession.shared.data(for: req)
        try validateHTTP(resp: resp, data: data)
    }

    private func validateHTTP(resp: URLResponse, data: Data) throws {
        guard let http = resp as? HTTPURLResponse else { return }
        if (200..<300).contains(http.statusCode) { return }
        if let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
           let msg = obj["error"] as? String {
            throw NSError(domain: "SubBar", code: http.statusCode, userInfo: [NSLocalizedDescriptionKey: msg])
        }
        throw NSError(domain: "SubBar", code: http.statusCode, userInfo: [NSLocalizedDescriptionKey: "HTTP \(http.statusCode)"])
    }
}
