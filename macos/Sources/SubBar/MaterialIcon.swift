import SwiftUI
import AppKit

// Filled 24px paths from google/material-design-icons 4.0.0 (Apache-2.0).
// Embedded so both `swift run` and the standalone menu bar app work offline.
enum MaterialIcon: String, CaseIterable {
    case chart = "M5 9.2h3V19H5zM10.6 5h2.8v14h-2.8zm5.6 8H19v6h-2.8z"
    case refresh = "M17.65 6.35C16.2 4.9 14.21 4 12 4c-4.42 0-7.99 3.58-7.99 8s3.57 8 7.99 8c3.73 0 6.84-2.55 7.73-6h-2.08c-.82 2.33-3.04 4-5.65 4-3.31 0-6-2.69-6-6s2.69-6 6-6c1.66 0 3.14.69 4.22 1.78L13 11h7V4l-2.35 2.35z"
    case add = "M19 13h-6v6h-2v-6H5v-2h6V5h2v6h6v2z"
    case pin = "M16,9V4l1,0c0.55,0,1-0.45,1-1v0c0-0.55-0.45-1-1-1H7C6.45,2,6,2.45,6,3v0c0,0.55,0.45,1,1,1l1,0v5c0,1.66-1.34,3-3,3h0v2h5.97v7l1,1l1-1v-7H19v-2h0C17.34,12,16,10.66,16,9z"
    case edit = "M3 17.25V21h3.75L17.81 9.94l-3.75-3.75L3 17.25zM20.71 7.04c.39-.39.39-1.02 0-1.41l-2.34-2.34c-.39-.39-1.02-.39-1.41 0l-1.83 1.83 3.75 3.75 1.83-1.83z"
    case open = "M19 19H5V5h7V3H5c-1.11 0-2 .9-2 2v14c0 1.1.89 2 2 2h14c1.1 0 2-.9 2-2v-7h-2v7zM14 3v2h3.59l-9.83 9.83 1.41 1.41L19 6.41V10h2V3h-7z"
    case link = "M3.9 12c0-1.71 1.39-3.1 3.1-3.1h4V7H7c-2.76 0-5 2.24-5 5s2.24 5 5 5h4v-1.9H7c-1.71 0-3.1-1.39-3.1-3.1zM8 13h8v-2H8v2zm9-6h-4v1.9h4c1.71 0 3.1 1.39 3.1 3.1s-1.39 3.1-3.1 3.1h-4V17h4c2.76 0 5-2.24 5-5s-2.24-5-5-5z"
    case check = "M9 16.17L4.83 12l-1.42 1.41L9 19 21 7l-1.41-1.41z"
    case warning = "M1 21h22L12 2 1 21zm12-3h-2v-2h2v2zm0-4h-2v-4h2v4z"

    private static let images = Dictionary(uniqueKeysWithValues: allCases.map { icon in
        let svg = #"<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24"><path d="\#(icon.rawValue)"/></svg>"#
        let image = NSImage(data: Data(svg.utf8))!
        image.isTemplate = true
        return (icon, image)
    })

    func glyph(_ size: CGFloat = 16) -> some View {
        Image(nsImage: Self.images[self]!).renderingMode(.template).resizable()
            .frame(width: size, height: size).accessibilityHidden(true)
    }

    func label(_ text: String, size: CGFloat = 16) -> some View {
        HStack(spacing: 4) { glyph(size); Text(text) }
    }
}

struct IconButton: View {
    let icon: MaterialIcon
    let title: String
    var selected = false
    var busy = false
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            Group {
                if busy { ProgressView().controlSize(.small) }
                else { icon.glyph() }
            }
            .foregroundStyle(selected ? HCTheme.bg : HCTheme.textSecondary)
            .frame(width: 28, height: 28)
            .background(selected ? HCTheme.accent : HCTheme.cardElevated, in: RoundedRectangle(cornerRadius: 6))
        }
        .buttonStyle(.plain)
        .disabled(busy)
        .accessibilityLabel(title)
        .help(title)
    }
}
