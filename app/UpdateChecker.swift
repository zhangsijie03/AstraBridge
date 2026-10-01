import Foundation
import CryptoKit
import CFNetwork

private struct GitHubReleasePayload: Decodable {
    let tagName: String
    let body: String?
    let assets: [GitHubAsset]

    private enum CodingKeys: String, CodingKey {
        case tagName = "tag_name"
        case body
        case assets
    }
}

private struct GitHubAsset: Decodable {
    let name: String
    let browserDownloadURL: String

    private enum CodingKeys: String, CodingKey {
        case name
        case browserDownloadURL = "browser_download_url"
    }
}

struct AppUpdate {
    let version: String
    let notes: String
    let archiveURL: URL
    let checksumURL: URL
}

struct StagedAppUpdate {
    let version: String
    let appURL: URL
    let stagingDirectory: URL
}

enum UpdateError: LocalizedError {
    case invalidRelease
    case unsupportedVersion(String)
    case missingAsset(String)
    case invalidDownloadURL
    case downloadFailed(String)
    case checksumMismatch
    case extractionFailed(String)
    case installLocationNotWritable
    case updaterLaunchFailed(String)

    var errorDescription: String? {
        switch self {
        case .invalidRelease: return "GitHub 返回的发行信息不完整。"
        case .unsupportedVersion(let value): return "发行版本号无效：\(value)"
        case .missingAsset(let name): return "发行包缺少文件：\(name)"
        case .invalidDownloadURL: return "更新下载地址不受信任。"
        case .downloadFailed(let message): return "更新下载失败：\(message)"
        case .checksumMismatch: return "更新包 SHA-256 校验失败，已停止安装。"
        case .extractionFailed(let message): return "更新包解压失败：\(message)"
        case .installLocationNotWritable: return "当前应用目录不可写，请将 AstraBridge 放入用户可写的应用目录后重试。"
        case .updaterLaunchFailed(let message): return "无法启动更新程序：\(message)"
        }
    }
}

final class UpdateChecker {
    private static let repository = "zhangsijie03/AstraBridge"
    private static let apiURL = URL(string: "https://api.github.com/repos/\(repository)/releases/latest")!
    private let session: URLSession

    init(session: URLSession? = nil) {
        if let session = session {
            self.session = session
        } else {
            let configuration = URLSessionConfiguration.default
            if let proxy = Self.environmentProxy() { configuration.connectionProxyDictionary = proxy }
            self.session = URLSession(configuration: configuration)
        }
    }

    func check(currentVersion: String, completion: @escaping (Result<AppUpdate?, Error>) -> Void) {
        var request = URLRequest(url: Self.apiURL)
        request.setValue("application/vnd.github+json", forHTTPHeaderField: "Accept")
        request.setValue("AstraBridge/\(currentVersion)", forHTTPHeaderField: "User-Agent")
        request.timeoutInterval = 15
        session.dataTask(with: request) { [weak self] data, response, error in
            guard let self = self else { return }
            if let error = error {
                self.finish(completion, .failure(UpdateError.downloadFailed(error.localizedDescription)))
                return
            }
            guard let http = response as? HTTPURLResponse, (200..<300).contains(http.statusCode), let data = data else {
                self.finish(completion, .failure(UpdateError.downloadFailed("GitHub API 返回了无效状态。")))
                return
            }
            do {
                let payload = try JSONDecoder().decode(GitHubReleasePayload.self, from: data)
                let versionParts = try Self.normalizeVersion(payload.tagName)
                let version = versionParts.map(String.init).joined(separator: ".")
                let current = try Self.normalizeVersion(currentVersion)
                guard Self.isNewer(versionParts, than: current) else {
                    self.finish(completion, .success(nil))
                    return
                }
                let archiveName = "AstraBridge-\(version)-macOS-arm64.zip"
                guard let archive = payload.assets.first(where: { $0.name == archiveName }),
                      let checksum = payload.assets.first(where: { $0.name == "SHA256SUMS.txt" }) else {
                    throw UpdateError.missingAsset(archiveName)
                }
                guard let archiveURL = Self.trustedAssetURL(archive.browserDownloadURL),
                      let checksumURL = Self.trustedAssetURL(checksum.browserDownloadURL) else {
                    throw UpdateError.invalidDownloadURL
                }
                let update = AppUpdate(version: version, notes: payload.body ?? "", archiveURL: archiveURL, checksumURL: checksumURL)
                self.finish(completion, .success(update))
            } catch {
                self.finish(completion, .failure(error))
            }
        }.resume()
    }

    func stage(_ update: AppUpdate, completion: @escaping (Result<StagedAppUpdate, Error>) -> Void) {
        let staging = FileManager.default.temporaryDirectory.appendingPathComponent("AstraBridge-update-\(UUID().uuidString)", isDirectory: true)
        do { try FileManager.default.createDirectory(at: staging, withIntermediateDirectories: true) }
        catch { finish(completion, .failure(UpdateError.downloadFailed(error.localizedDescription))); return }
        let archive = staging.appendingPathComponent("update.zip")
        let checksum = staging.appendingPathComponent("SHA256SUMS.txt")
        download(update.archiveURL, to: archive) { [weak self] result in
            guard let self = self else { return }
            switch result {
            case .failure(let error): self.cleanup(staging); self.finish(completion, .failure(error))
            case .success:
                self.download(update.checksumURL, to: checksum) { checksumResult in
                    switch checksumResult {
                    case .failure(let error): self.cleanup(staging); self.finish(completion, .failure(error))
                    case .success:
                        do {
                            try self.verify(archive: archive, checksumFile: checksum, expectedName: "AstraBridge-\(update.version)-macOS-arm64.zip")
                            let extracted = staging.appendingPathComponent("extracted", isDirectory: true)
                            try self.extract(archive, to: extracted)
                            let appURL = extracted.appendingPathComponent("AstraBridge.app", isDirectory: true)
                            guard FileManager.default.fileExists(atPath: appURL.path) else { throw UpdateError.extractionFailed("未找到 AstraBridge.app。") }
                            self.finish(completion, .success(StagedAppUpdate(version: update.version, appURL: appURL, stagingDirectory: staging)))
                        } catch {
                            self.cleanup(staging); self.finish(completion, .failure(error))
                        }
                    }
                }
            }
        }
    }

    func launchUpdater(staged: StagedAppUpdate, replacing target: URL, waitingFor pid: Int32) throws {
        let script = staged.stagingDirectory.appendingPathComponent("apply-update.sh")
        let contents = """
        #!/bin/sh
        set -eu
        target="$1"
        source="$2"
        pid="$3"
        cleanup="$4"
        tries=0
        while kill -0 "$pid" 2>/dev/null; do
          tries=$((tries + 1))
          if [ "$tries" -gt 240 ]; then exit 10; fi
          sleep 0.25
        done
        sleep 0.5
        temporary="$target.update"
        if ditto "$source" "$temporary" 2>/dev/null; then
          rm -rf "$target"
          mv "$temporary" "$target"
        else
          /usr/bin/osascript - "$source" "$target" <<'APPLESCRIPT'
        on run argv
          set sourcePath to item 1 of argv
          set targetPath to item 2 of argv
          do shell script "/bin/rm -rf " & quoted form of targetPath & " && /usr/bin/ditto " & quoted form of sourcePath & " " & quoted form of targetPath with administrator privileges
        end run
        APPLESCRIPT
        fi
        if [ ! -d "$target" ]; then exit 11; fi
        open "$target"
        rm -rf "$cleanup"
        rm -f "$0"
        """
        do {
            try contents.write(to: script, atomically: true, encoding: .utf8)
            try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: script.path)
            let process = Process()
            process.executableURL = URL(fileURLWithPath: "/bin/sh")
            process.arguments = [script.path, target.path, staged.appURL.path, String(pid), staged.stagingDirectory.path]
            process.standardOutput = FileHandle.nullDevice
            process.standardError = FileHandle.nullDevice
            try process.run()
        } catch { throw UpdateError.updaterLaunchFailed(error.localizedDescription) }
    }

    func discard(_ staged: StagedAppUpdate) {
        cleanup(staged.stagingDirectory)
    }

    private func download(_ url: URL, to destination: URL, completion: @escaping (Result<Void, Error>) -> Void) {
        var request = URLRequest(url: url); request.timeoutInterval = 120
        session.downloadTask(with: request) { temporaryURL, response, error in
            if let error = error { self.finish(completion, .failure(UpdateError.downloadFailed(error.localizedDescription))); return }
            guard let temporaryURL = temporaryURL, let http = response as? HTTPURLResponse, (200..<300).contains(http.statusCode) else {
                self.finish(completion, .failure(UpdateError.downloadFailed("下载服务器返回了无效状态。"))); return
            }
            do {
                try FileManager.default.removeItemIfExists(at: destination)
                try FileManager.default.copyItem(at: temporaryURL, to: destination)
                self.finish(completion, .success(()))
            } catch { self.finish(completion, .failure(UpdateError.downloadFailed(error.localizedDescription))) }
        }.resume()
    }

    private func verify(archive: URL, checksumFile: URL, expectedName: String) throws {
        let lines = try String(contentsOf: checksumFile, encoding: .utf8).split(whereSeparator: \.isNewline)
        guard let line = lines.first(where: { $0.contains(expectedName) }) else { throw UpdateError.missingAsset(expectedName) }
        let expected = String(line.split(whereSeparator: { $0 == " " || $0 == "\t" }).first ?? "").lowercased()
        let handle = try FileHandle(forReadingFrom: archive)
        var hasher = SHA256()
        while autoreleasepool(invoking: { () -> Bool in
            let data = handle.readData(ofLength: 1024 * 1024)
            if data.isEmpty { return false }
            hasher.update(data: data); return true
        }) {}
        try handle.close()
        let actual = hasher.finalize().map { String(format: "%02x", $0) }.joined()
        guard actual == expected else { throw UpdateError.checksumMismatch }
    }

    private func extract(_ archive: URL, to destination: URL) throws {
        try FileManager.default.createDirectory(at: destination, withIntermediateDirectories: true)
        let process = Process(); process.executableURL = URL(fileURLWithPath: "/usr/bin/ditto")
        process.arguments = ["-x", "-k", archive.path, destination.path]
        process.standardOutput = FileHandle.nullDevice; process.standardError = FileHandle.nullDevice
        try process.run(); process.waitUntilExit()
        guard process.terminationStatus == 0 else { throw UpdateError.extractionFailed("ditto 返回状态 \(process.terminationStatus)。") }
    }

    private static func normalizeVersion(_ raw: String) throws -> [Int] {
        let value = raw.trimmingCharacters(in: .whitespacesAndNewlines).replacingOccurrences(of: "^v", with: "", options: .regularExpression)
        let parts = value.split(separator: ".")
        let numbers = parts.compactMap { Int($0) }
        guard parts.count == 3, numbers.count == 3, numbers.allSatisfy({ $0 >= 0 }) else { throw UpdateError.unsupportedVersion(raw) }
        return numbers
    }

    private static func isNewer(_ lhs: [Int], than rhs: [Int]) -> Bool {
        for index in 0..<min(lhs.count, rhs.count) {
            if lhs[index] != rhs[index] { return lhs[index] > rhs[index] }
        }
        return lhs.count > rhs.count
    }

    private static func trustedAssetURL(_ raw: String) -> URL? {
        guard let url = URL(string: raw), url.scheme == "https", url.host == "github.com", url.path.contains("/\(repository)/releases/download/") else { return nil }
        return url
    }

    private static func environmentProxy() -> [AnyHashable: Any]? {
        let environment = ProcessInfo.processInfo.environment
        guard let raw = environment["HTTPS_PROXY"] ?? environment["https_proxy"] ?? environment["HTTP_PROXY"] ?? environment["http_proxy"],
              let url = URL(string: raw), let host = url.host else { return nil }
        let httpPort = url.port ?? 80
        let httpsPort = url.port ?? 443
        return [
            kCFNetworkProxiesHTTPEnable as String: true,
            kCFNetworkProxiesHTTPProxy as String: host,
            kCFNetworkProxiesHTTPPort as String: httpPort,
            kCFNetworkProxiesHTTPSEnable as String: true,
            kCFNetworkProxiesHTTPSProxy as String: host,
            kCFNetworkProxiesHTTPSPort as String: httpsPort
        ]
    }

    private func finish<T>(_ completion: @escaping (Result<T, Error>) -> Void, _ result: Result<T, Error>) {
        DispatchQueue.main.async { completion(result) }
    }

    private func cleanup(_ directory: URL) { try? FileManager.default.removeItem(at: directory) }
}

private extension FileManager {
    func removeItemIfExists(at url: URL) throws {
        if fileExists(atPath: url.path) { try removeItem(at: url) }
    }
}
