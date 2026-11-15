import Foundation

/// Launches the `subbar-server` binary bundled inside SubBar.app (Contents/Resources/subbar-server)
/// so the app works standalone. If a server already answers on the loopback port
/// (e.g. a launchd-managed instance), nothing is spawned.
final class ServerLauncher {
    private var process: Process?
    private let host = "127.0.0.1"
    private let port = 8328

    private var bundledBinary: URL? {
        let url = Bundle.main.resourceURL?.appendingPathComponent("subbar-server")
        guard let url, FileManager.default.isExecutableFile(atPath: url.path) else { return nil }
        return url
    }

    func startIfNeeded() {
        guard process == nil, let binary = bundledBinary, !isPortOpen() else { return }
        let p = Process()
        p.executableURL = binary
        p.arguments = ["-addr", "\(host):\(port)"]
        p.standardOutput = FileHandle.nullDevice
        p.standardError = FileHandle.nullDevice
        do {
            try p.run()
            process = p
        } catch {
            NSLog("SubBar: failed to start bundled server: \(error)")
        }
    }

    func stop() {
        guard let p = process, p.isRunning else { return }
        p.terminate()
        process = nil
    }

    private func isPortOpen() -> Bool {
        let fd = socket(AF_INET, SOCK_STREAM, 0)
        guard fd >= 0 else { return false }
        defer { close(fd) }
        var addr = sockaddr_in()
        addr.sin_family = sa_family_t(AF_INET)
        addr.sin_port = in_port_t(port).bigEndian
        addr.sin_addr.s_addr = inet_addr(host)
        let result = withUnsafePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                connect(fd, $0, socklen_t(MemoryLayout<sockaddr_in>.size))
            }
        }
        return result == 0
    }
}
