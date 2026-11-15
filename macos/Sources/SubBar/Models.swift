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
        if let wins = windows, !wins.isEmpty {
            let nonCycle = wins.filter { $0.key != "cycle" }
            if nonCycle.isEmpty {
                return primaryMetric + abnormalMark
            }
            let shortWins = nonCycle.filter { ($0.windowSeconds ?? 18000) <= 86400 }
            let longWins = nonCycle.filter { ($0.windowSeconds ?? 0) >= 5 * 86400 }

            func mostConsumed(_ list: [QuotaWindow]) -> QuotaWindow? {
                guard var best = list.first else { return nil }
                for item in list.dropFirst() where item.usedPercent > best.usedPercent {
                    best = item
                }
                return best
            }

            if let sw = mostConsumed(shortWins), let lw = mostConsumed(longWins) {
                let sv = showRemaining ? sw.remainingPercent : sw.usedPercent
                let lv = showRemaining ? lw.remainingPercent : lw.usedPercent
                return String(format: "%.0f%%/%.0f%%%@", sv, lv, abnormalMark)
            }

            if let w = mostConsumed(nonCycle) {
                let v = showRemaining ? w.remainingPercent : w.usedPercent
                return String(format: "%.0f%%%@", v, abnormalMark)
            }
        }
        return primaryMetric + abnormalMark
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

// High-Contrast Palette (tailored for maximum legibility)
enum HCTheme {
    static let bg = Color(red: 0.024, green: 0.035, blue: 0.055)          // #06090E
    static let card = Color(red: 0.067, green: 0.094, blue: 0.137)        // #111823
    static let cardElevated = Color(red: 0.102, green: 0.141, blue: 0.20) // #1A2433
    static let border = Color(red: 0.24, green: 0.31, blue: 0.41)         // #3D4F69
    static let textPrimary = Color.white
    static let textSecondary = Color(red: 0.89, green: 0.92, blue: 0.96)  // #E3EBF5
    static let textMuted = Color(red: 0.76, green: 0.82, blue: 0.89)      // #C2D1E3
    static let accent = Color(red: 0.176, green: 0.831, blue: 0.749)      // #2DD4BF
    static let ok = Color(red: 0.133, green: 0.773, blue: 0.369)          // #22C55E
    static let warn = Color(red: 0.984, green: 0.749, blue: 0.141)        // #FBBF24
    static let crit = Color(red: 0.973, green: 0.443, blue: 0.443)        // #F87171

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
