import Foundation
import SwiftUI

enum MenuBarDisplayStyle: String, CaseIterable, Identifiable {
    case stacked = "stacked"
    case inline = "inline"
    case gauge = "gauge"
    case iconOnly = "iconOnly"

    var id: String { rawValue }

    var label: String {
        switch self {
        case .stacked: return "双行置顶 (仿 CodexBar)"
        case .inline: return "单行紧凑指标"
        case .gauge: return "迷你进度条 + 指标"
        case .iconOnly: return "极简图标 + 状态点"
        }
    }
}

struct SnapshotResponse: Codable {
    let generatedAt: String
    let summary: SummaryStats
    let entries: [EntrySnapshot]
}

struct SummaryStats: Codable {
    let totalEntries: Int
    let enabledEntries: Int
    let subscriptionCount: Int
    let balanceCount: Int
    let warningCount: Int
    let criticalCount: Int
    let availableBalances: [CurrencyTotal]
    let monthlyFixedCosts: [CurrencyTotal]
}

struct CurrencyTotal: Codable, Identifiable {
    var id: String { currency }
    let currency: String
    let symbol: String
    let amount: Double
}

struct QuotaWindow: Codable, Identifiable {
    var id: String { key + "_" + label }
    let key: String
    let label: String
    let usedPercent: Double
    let remainingPercent: Double
    let usedDisplay: String?
    let resetsAt: String?
    let windowSeconds: Int64?
    let paceDeltaPercent: Double?
}

struct KVDetail: Codable, Identifiable {
    var id: String { label }
    let label: String
    let value: String
}

struct BalanceInfo: Codable {
    let currency: String
    let symbol: String
    let available: Double
    let used: Double?
    let limit: Double?
    let lowThreshold: Double?
    let isAvailable: Bool?
    let details: [KVDetail]?
}

struct EntrySnapshot: Codable, Identifiable {
    let id: String
    let name: String
    let provider: String
    let providerLabel: String
    let kind: String // "subscription" | "balance"
    let badge: String
    let enabled: Bool
    let pinned: Bool
    let sortOrder: Int
    let planLabel: String?
    let accountEmail: String?
    let dashboardUrl: String?
    let notes: String?
    let monthlyCost: Double?
    let costCurrency: String?
    let status: String // "ok" | "warning" | "critical" | "stale" | "error" | "disabled"
    let statusReason: String?
    let primaryMetric: String
    let windows: [QuotaWindow]?
    let balance: BalanceInfo?
    let fetchedAt: String?
    let nextRefreshAt: String?
    let error: String?

    var hasQuotaOrBalance: Bool {
        if let w = windows, !w.isEmpty { return true }
        if balance != nil { return true }
        return false
    }

    var isInitialError: Bool {
        !hasQuotaOrBalance && status == "error"
    }

    // When the compact title has two percentages, the gauge represents the first.
    private var menuWindows: [QuotaWindow] {
        let quota = (windows ?? []).filter { $0.key != "cycle" }
        func mostUsed(_ list: [QuotaWindow]) -> QuotaWindow? {
            list.max { $0.usedPercent < $1.usedPercent }
        }
        if let short = mostUsed(quota.filter { ($0.windowSeconds ?? 18000) <= 86400 }),
           let long = mostUsed(quota.filter { ($0.windowSeconds ?? 0) >= 5 * 86400 }) {
            return [short, long]
        }
        return mostUsed(quota).map { [$0] } ?? []
    }

    func menuGaugeFraction(showRemaining: Bool) -> Double? {
        let fraction: Double
        if kind == "balance", let b = balance {
            guard let limit = b.limit, limit > 0 else { return nil }
            fraction = b.available / limit
        } else if let w = menuWindows.first {
            fraction = (showRemaining ? w.remainingPercent : w.usedPercent) / 100
        } else if let cycle = windows?.first(where: { $0.key == "cycle" }) {
            fraction = cycle.usedPercent / 100
        } else { return nil }
        return max(0, min(1, fraction))
    }

    func barCompactValue(showRemaining: Bool) -> String {
        if !enabled { return "OFF" }
        if !hasQuotaOrBalance && status == "error" { return "ERR" }
        let abnormalMark = (status == "error" || status == "stale" || (error?.isEmpty == false)) ? "!" : ""
        if kind == "balance", let b = balance {
            if b.available >= 100 {
                return "\(b.symbol)\(Int(b.available.rounded()))\(abnormalMark)"
            }
            return String(format: "%@%.1f%@", b.symbol, b.available, abnormalMark)
        }
        let readings = menuWindows.map { String(format: "%.0f%%", showRemaining ? $0.remainingPercent : $0.usedPercent) }
        return (readings.isEmpty ? primaryMetric : readings.joined(separator: "/")) + abnormalMark
    }
}

struct OptionItem: Codable, Identifiable, Hashable {
    var id: String { value }
    let value: String
    let label: String
}

struct ProviderFieldSchema: Codable, Identifiable {
    var id: String { key }
    let key: String
    let label: String
    let type: String
    let placeholder: String?
    let help: String?
    let required: Bool?
    let `default`: String?
    let options: [OptionItem]?
}

struct ProviderDescriptor: Codable, Identifiable {
    let id: String
    let name: String
    let defaultBadge: String
    let defaultKind: String
    let supportsKindSwitch: Bool?
    let supportsOAuthFlow: Bool?
    let oauthButtonLabel: String?
    let defaultRefreshSeconds: Int
    let defaultDashboardUrl: String?
    let description: String
    let fields: [ProviderFieldSchema]?
}

struct ProvidersEnvelope: Codable {
    let providers: [ProviderDescriptor]
}

struct EntryConfigPublic: Codable, Identifiable {
    let id: String
    let name: String
    let provider: String
    let kind: String
    let badge: String?
    let enabled: Bool
    let pinned: Bool
    let sortOrder: Int
    let refreshSeconds: Int
    let dashboardUrl: String?
    let notes: String?
    let monthlyCost: Double?
    let costCurrency: String?
    let authMode: String?
    let accountEmail: String?
    let credentialFile: String?
    let accountId: String?
    let baseUrl: String?
    let method: String?
    let authHeader: String?
    let authPrefix: String?
    let currency: String?
    let scale: Double?
    let lowThreshold: Double?
    let warnPercent: Double?
    let balancePath: String?
    let usedPath: String?
    let limitPath: String?
    let usedPercentPath: String?
    let resetAtPath: String?
    let planPath: String?
    let manualCycle: String?
    let billingDay: Int?
    let nextResetDate: String?
    let planLabel: String?
    let quotaUsed: Double?
    let quotaTotal: Double?
    let quotaUnit: String?
    let manualBalance: Double?
    let hasSecret: Bool
    let hasRefreshToken: Bool?
    let secretHint: String?
}

struct EntriesEnvelope: Codable {
    let entries: [EntryConfigPublic]
}

struct DiscoveryHint: Codable, Identifiable {
    let id: String
    let provider: String
    let suggestedName: String
    let note: String
}

struct DiscoverEnvelope: Codable {
    let hints: [DiscoveryHint]?
}

struct OAuthStartResponse: Codable {
    let state: String
    let provider: String
    let authUrl: String
    let callbackPort: Int
    let callbackMode: String
    let userCode: String?
    let expiresAt: String
}

struct OAuthStatusResponse: Codable {
    let state: String
    let provider: String
    let status: String
    let error: String?
    let accountEmail: String?
    let entry: EntryConfigPublic?
    let snapshot: EntrySnapshot?
}

// Everforest Dark: warm gold for actions, forest green for healthy usage.
enum HCTheme {
    private static func rgb(_ hex: UInt32) -> Color {
        Color(.sRGB, red: Double(hex >> 16) / 255, green: Double((hex >> 8) & 255) / 255, blue: Double(hex & 255) / 255)
    }

    static let bg = rgb(0x2d353b)
    static let card = rgb(0x343f44)
    static let cardElevated = rgb(0x3d484d)
    static let border = rgb(0x56635d)
    static let textPrimary = rgb(0xd3c6aa)
    static let textSecondary = rgb(0xc1c7b5)
    static let textMuted = rgb(0xa2afa1)
    static let accent = rgb(0xe6c384)
    static let ok = rgb(0x83c995)
    static let warn = rgb(0xe69875)
    static let crit = rgb(0xe67e80)

    static func statusColor(_ status: String) -> Color {
        switch status {
        case "ok": return ok
        case "warning", "stale": return warn
        case "critical", "error": return crit
        default: return textMuted
        }
    }
}

enum TimeFmt {
    private static let isoFrac: ISO8601DateFormatter = {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return f
    }()

    private static let isoPlain: ISO8601DateFormatter = {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime]
        return f
    }()

    static func parseISO(_ raw: String?) -> Date? {
        guard let raw = raw, !raw.isEmpty else { return nil }
        return isoFrac.date(from: raw) ?? isoPlain.date(from: raw)
    }

    static func countdown(to raw: String?, now: Date = Date()) -> String {
        guard let target = parseISO(raw) else { return "" }
        let sec = Int(target.timeIntervalSince(now))
        if sec <= 0 { return "即将重置" }
        let d = sec / 86400
        let h = (sec % 86400) / 3600
        let m = (sec % 3600) / 60
        if d > 0 { return "\(d)天 \(h)小时后重置" }
        if h > 0 { return "\(h)小时 \(m)分后重置" }
        return "\(max(1, m))分钟后重置"
    }

    static func relativeAgo(_ raw: String?, now: Date = Date()) -> String {
        guard let d = parseISO(raw) else { return "未刷新" }
        let sec = max(0, Int(now.timeIntervalSince(d)))
        if sec < 10 { return "刚刚" }
        if sec < 60 { return "\(sec)秒前" }
        let m = sec / 60
        if m < 60 { return "\(m)分钟前" }
        return "\(m / 60)小时前"
    }
}
